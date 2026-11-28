package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/voicestate"
	"github.com/asheshgoplani/agent-deck/internal/voicestream"
)

// `agent-deck voice relay` runs next to the voice daemon. It publishes
// voice.state frames on this host's bus and forwards every change to the
// stream hosts (registered remotes), where `voice mirror-in` writes
// mirror.json and publishes the same frame on that host's bus. Heard and
// spoken text never crosses hosts: the mirror has no such fields.

func init() {
	voiceSubs = append(voiceSubs,
		voiceSub{
			name:    "relay",
			summary: "Publish voice state on the bus and forward it to stream hosts",
			usage:   "  agent-deck voice relay --to <remote>[,<remote>...] --self <name> [--dir <voice dir>]\n           --self is this machine's name in the remotes' own [remotes.<name>] config",
			run:     runVoiceRelay,
		},
		voiceSub{
			name:    "mirror-in",
			summary: "Receive the relay's voice state on stdin (run by voice relay over ssh)",
			usage:   "  agent-deck voice mirror-in [--dir <voice dir>]   (one mirror JSON object per line on stdin)",
			run:     runVoiceMirrorIn,
		},
	)
}

func runVoiceRelay(profile string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("voice relay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	to := fs.String("to", "", "comma-separated remote names to forward to (required: session titles and ids go there)")
	self := fs.String("self", "", "this machine's name as the remotes' configs name it (required: a target on this machine is shown under it there)")
	dir := fs.String("dir", "", "voice state dir (default: $CLAUDE_VOICE_STATE_DIR or ~/.claude/voice)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "voice relay: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	vdir := *dir
	if vdir == "" {
		vdir = voicestate.DefaultDir()
	}
	name := strings.TrimSpace(*self)
	if name == "" || strings.TrimSpace(*to) == "" {
		fmt.Fprintln(stderr, "voice relay: --to and --self are required")
		return 2
	}
	profile = session.GetEffectiveProfile(profile)
	cfg, err := session.LoadUserConfig()
	if err != nil {
		fmt.Fprintln(stderr, "voice relay: loading config:", err)
		return 1
	}
	var dests []string
	for _, n := range strings.Split(*to, ",") {
		if n = strings.TrimSpace(n); n == "" {
			continue
		}
		if _, ok := cfg.Remotes[n]; !ok {
			fmt.Fprintf(stderr, "voice relay: remote %q not found\n", n)
			return 2
		}
		dests = append(dests, n)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var wg sync.WaitGroup
	defer func() { _ = events.CloseDefault() }()
	defer wg.Wait()

	// Local frames: one producer per host (lock), this one or the web server's.
	prod := voicestream.NewProducer(vdir, profile)
	prod.Log = stderr
	prod.Origin = name
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := voicestream.RunExclusive(ctx, profile, prod); err != nil {
			fmt.Fprintln(stderr, "voice relay: local producer:", err)
		}
	}()

	// The links outlive the relay loop by one flush: on a stop the relay
	// sends "off" and each link writes it before it closes (B2).
	linkCtx, stopLinks := context.WithCancel(context.Background())
	defer stopLinks()
	links := make([]*relayLink, 0, len(dests))
	for _, d := range dests {
		l := newRelayLink(d, session.NewSSHRunner(d, cfg.Remotes[d]), stderr)
		links = append(links, l)
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.run(linkCtx)
		}()
	}
	fmt.Fprintf(stdout, "voice relay: dir %s, self %q, forwarding to %d host(s): %s\n", vdir, name, len(dests), strings.Join(dests, ", "))

	sinks := make([]voicestream.Sink, 0, len(links))
	for _, l := range links {
		sinks = append(sinks, voicestream.Sink{Dest: l.name, Send: l.offer, Ping: l.ping})
	}
	relay := &voicestream.Relay{Reader: voicestate.NewReader(vdir), Self: name, Sinks: sinks}
	relay.Run(ctx) // returns on SIGINT/SIGTERM after offering "off" to every link
	for _, l := range links {
		if !l.flush(2 * time.Second) {
			fmt.Fprintf(stderr, "voice relay: %s: could not send off before stopping\n", l.name)
		}
	}
	stopLinks()
	return 0
}

// relayStreamRunner is the part of session.SSHRunner a relay link uses.
type relayStreamRunner interface {
	RunStdinStreamIO(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) error
}

// relayLink is one stream host: a long-lived `voice mirror-in` over ssh,
// fed the newest mirror only (an old one is replaced, never queued).
type relayLink struct {
	name   string
	runner relayStreamRunner
	latest chan voicestate.Mirror
	pings  chan struct{}
	stderr io.Writer
	mu     sync.Mutex
	last   *voicestate.Mirror
	// offered counts offers; written is the offer number of the newest
	// mirror written to a live link (flush waits for written == offered).
	offered, written uint64
}

func newRelayLink(name string, runner relayStreamRunner, stderr io.Writer) *relayLink {
	return &relayLink{name: name, runner: runner, latest: make(chan voicestate.Mirror, 1), pings: make(chan struct{}, 1), stderr: stderr}
}

// relayBackoffMax caps the reconnect wait: a remote is right again about a
// second after its link returns.
const relayBackoffMax = time.Second

// flush waits up to timeout until the newest offered mirror is written to
// a live link (the shutdown "off"). It reports whether it was.
func (l *relayLink) flush(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		l.mu.Lock()
		done := l.written >= l.offered
		l.mu.Unlock()
		if done {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ping asks the link to send a keepalive line (nothing to send).
func (l *relayLink) ping() {
	select {
	case l.pings <- struct{}{}:
	default:
	}
}

func (l *relayLink) offer(m voicestate.Mirror) {
	l.mu.Lock()
	l.last = &m
	l.offered++
	l.mu.Unlock()
	select {
	case l.latest <- m:
	default:
		select {
		case <-l.latest:
		default:
		}
		select {
		case l.latest <- m:
		default:
		}
	}
}

func (l *relayLink) run(ctx context.Context) {
	backoff := 250 * time.Millisecond
	for ctx.Err() == nil {
		started := time.Now()
		err := l.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > 10*time.Second {
			backoff = 250 * time.Millisecond // it was up: start over fast
		}
		fmt.Fprintf(l.stderr, "voice relay: %s: link ended (%v), reconnecting in %s\n", l.name, err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > relayBackoffMax {
			backoff = relayBackoffMax
		}
	}
}

// session runs one ssh connection until it fails or ctx ends.
func (l *relayLink) session(ctx context.Context) error {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- l.runner.RunStdinStreamIO(sctx, pr, io.Discard, l.stderr, "voice", "mirror-in")
		_ = pr.Close()
	}()
	// write sends one mirror and records it as written; the offer number is
	// read with the mirror, so a newer offer is never counted as sent.
	write := func(m voicestate.Mirror, seq uint64) error {
		raw, err := json.Marshal(m)
		if err != nil {
			return err
		}
		if _, err = pw.Write(append(raw, '\n')); err != nil {
			return err
		}
		l.mu.Lock()
		if seq > l.written {
			l.written = seq
		}
		l.mu.Unlock()
		return nil
	}
	newest := func() (*voicestate.Mirror, uint64) {
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.last, l.offered
	}
	// A new link starts with the newest state.
	if first, seq := newest(); first != nil {
		if err := write(*first, seq); err != nil {
			_ = pw.Close()
			return <-done
		}
	}
	for {
		select {
		case <-ctx.Done():
			_ = pw.Close()
			<-done
			return nil
		case err := <-done:
			_ = pw.Close()
			if err == nil {
				err = errors.New("remote receiver exited")
			}
			return err
		case <-l.latest:
			if m, seq := newest(); m != nil {
				if err := write(*m, seq); err != nil {
					_ = pw.Close()
					return <-done
				}
			}
		case <-l.pings:
			if _, err := io.WriteString(pw, voicestream.PingLine+"\n"); err != nil {
				_ = pw.Close()
				return <-done
			}
		}
	}
}

func runVoiceMirrorIn(profile string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("voice mirror-in", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "", "voice state dir (default: $CLAUDE_VOICE_STATE_DIR or ~/.claude/voice)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	vdir := *dir
	if vdir == "" {
		vdir = voicestate.DefaultDir()
	}
	profile = session.GetEffectiveProfile(profile)
	// A hang-up is the relay's link going away (network cut): keep the
	// mirror's producer up until the mirror expires or a new link takes
	// over, instead of dying with the connection.
	signal.Ignore(syscall.SIGHUP)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	defer func() { _ = events.CloseDefault() }()
	if err := voicestream.MirrorIn(ctx, profile, vdir, os.Stdin, stderr); err != nil {
		fmt.Fprintln(stderr, "voice mirror-in:", err)
		return 1
	}
	return 0
}
