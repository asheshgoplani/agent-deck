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
			usage:   "  agent-deck voice relay [--to <remote>[,<remote>...]] [--self <name>] [--dir <voice dir>]",
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
	to := fs.String("to", "", "comma-separated remote names to forward to (default: every configured remote)")
	self := fs.String("self", "", "this machine's name as the remotes know it (default: $AGENTDECK_HOSTNAME or the short hostname)")
	dir := fs.String("dir", "", "voice state dir (default: $CLAUDE_VOICE_STATE_DIR or ~/.claude/voice)")
	if err := fs.Parse(args); err != nil {
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
	if name == "" {
		name = voicestream.LocalOrigin()
	}
	profile = session.GetEffectiveProfile(profile)
	cfg, err := session.LoadUserConfig()
	if err != nil {
		fmt.Fprintln(stderr, "voice relay: loading config:", err)
		return 1
	}
	var dests []string
	if strings.TrimSpace(*to) == "" {
		for n := range cfg.Remotes {
			dests = append(dests, n)
		}
	} else {
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
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var wg sync.WaitGroup
	defer func() { _ = events.CloseDefault() }()
	defer wg.Wait()

	// Local frames: one producer per host (lock), this one or the web server's.
	prod := voicestream.NewProducer(vdir, profile)
	prod.Origin = name
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := voicestream.RunExclusive(ctx, profile, prod); err != nil {
			fmt.Fprintln(stderr, "voice relay: local producer:", err)
		}
	}()

	links := make([]*relayLink, 0, len(dests))
	for _, d := range dests {
		l := &relayLink{name: d, rc: cfg.Remotes[d], latest: make(chan voicestate.Mirror, 1), stderr: stderr}
		links = append(links, l)
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.run(ctx)
		}()
	}
	fmt.Fprintf(stdout, "voice relay: dir %s, self %q, forwarding to %d host(s): %s\n", vdir, name, len(dests), strings.Join(dests, ", "))

	sinks := make([]voicestream.Sink, 0, len(links))
	for _, l := range links {
		sinks = append(sinks, voicestream.Sink{Dest: l.name, Send: l.offer})
	}
	relay := &voicestream.Relay{Reader: voicestate.NewReader(vdir), Self: name, Sinks: sinks}
	relay.Run(ctx)
	return 0
}

// relayLink is one stream host: a long-lived `voice mirror-in` over ssh,
// fed the newest mirror only (an old one is replaced, never queued).
type relayLink struct {
	name   string
	rc     session.RemoteConfig
	latest chan voicestate.Mirror
	stderr io.Writer
	mu     sync.Mutex
	last   *voicestate.Mirror
}

func (l *relayLink) offer(m voicestate.Mirror) {
	l.mu.Lock()
	l.last = &m
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
			backoff = 250 * time.Millisecond
		}
		fmt.Fprintf(l.stderr, "voice relay: %s: link ended (%v), reconnecting in %s\n", l.name, err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

// session runs one ssh connection until it fails or ctx ends.
func (l *relayLink) session(ctx context.Context) error {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pr, pw := io.Pipe()
	runner := session.NewSSHRunner(l.name, l.rc)
	done := make(chan error, 1)
	go func() {
		done <- runner.RunIO(sctx, pr, io.Discard, l.stderr, "voice", "mirror-in")
		_ = pr.Close()
	}()
	write := func(m voicestate.Mirror) error {
		raw, err := json.Marshal(m)
		if err != nil {
			return err
		}
		_, err = pw.Write(append(raw, '\n'))
		return err
	}
	// A new link starts with the newest state.
	l.mu.Lock()
	first := l.last
	l.mu.Unlock()
	if first != nil {
		if err := write(*first); err != nil {
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
		case m := <-l.latest:
			if err := write(m); err != nil {
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
		return 2
	}
	vdir := *dir
	if vdir == "" {
		vdir = voicestate.DefaultDir()
	}
	profile = session.GetEffectiveProfile(profile)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	defer func() { _ = events.CloseDefault() }()
	if err := voicestream.MirrorIn(ctx, profile, vdir, os.Stdin, stderr); err != nil {
		fmt.Fprintln(stderr, "voice mirror-in:", err)
		return 1
	}
	return 0
}
