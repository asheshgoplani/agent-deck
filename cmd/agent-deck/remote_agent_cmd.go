package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

// `agent-deck remote-agent` is the remote end of a persistent channel
// (#2174). A local TUI opens ONE ssh session per remote to it and keeps it
// open: requests arrive as JSON lines on stdin ({"id":1,"args":["list",
// "--json"]}), answers leave as JSON lines on stdout ({"id":1,"stdout":"...",
// "stderr":"...","code":0}). Between answers the agent pushes
// {"event":"changed"} whenever the profile's state.db changes, so the TUI
// refetches at once instead of at its next poll.
//
// Each request runs this same binary as a subprocess with the given args, so
// the answer is byte-for-byte what `ssh host agent-deck <args>` would print;
// only the ssh handshake and channel setup per command are gone, and the
// change feed is new. Requests run concurrently; answers carry their id.
// At most remoteAgentMaxConcurrent subprocesses run at once (the rest wait
// their turn), identical read-only requests that overlap share one run and
// each get the answer under their own id, and {"id":N,"cancel":true} kills
// the subprocess of request N (no answer follows for a cancelled request).
//
// The change feed's probe does not fork: it keeps the profile's storage open
// in this process and builds the two listings through the same functions
// `list --json` and `group list --json` print through (buildListJSON,
// buildGroupListJSON). Booting the binary twice per change, each opening
// storage and refreshing every status, was most of the second between a
// change on the remote and the local screen; the event carries the probe's
// duration as probe_ms so that cost stays visible. The probe is read-only:
// this process never registers a global state DB, so the last-activity
// flush a status refresh would otherwise do is a no-op here, and a probe
// never moves the stamp it watches. Stamp changes are debounced: a probe
// runs no sooner than remoteAgentProbeQuiet after the previous one ended,
// and a change landing while a probe runs is probed again instead of being
// taken as seen (safe only because the probe is read-only: nothing it does
// can move the stamp and chain probes).
//
// The listings travel compacted (json.Marshal form, no indentation): the
// same JSON documents `list --json` and `group list --json` print, minus
// whitespace, so the local parser sees the same shape. A "changed" event
// whose listings would exceed remoteAgentMaxPushBytes, whose probe failed,
// or whose listings do not look like a JSON array and object, is pushed
// bare (no listings) so the local side fetches instead of applying junk.
// Each "changed" event and each command reply carries "stamp": the state
// DB's newest mtime (remote clock, unix nanoseconds) when the listing was
// taken, or after the command finished. A pushed listing whose stamp is
// older than the stamp of a mutating command's reply predates that command.
//
// Liveness: the agent pushes {"event":"ping"} every remoteAgentPingEvery
// and exits when a write to stdout fails, when its outbound queue stays
// full for remoteAgentWriteStall (the peer stopped reading), or when
// nothing arrives on stdin for
// remoteAgentIdleAfter. Any line counts as inbound: a request, a cancel or
// {"id":N,"ping":true} (answered with {"id":N,"code":0}; without an id it
// is silently absorbed). Writes go through a bounded queue drained by one
// goroutine, so a dead pipe can hold the watcher or a reply for at most
// that stall, never for good; a slow live pipe merely backpressures them.
//
// Pane watch (#2177 follow-up): {"id":2,"watch":"<sessionID>","lines":200}
// asks the agent to follow one session's tmux pane. The agent captures it
// in-process every ~300ms, keeps the last "lines" lines (all of them when
// the field is absent) and pushes {"event":"pane","session":"<id>",
// "stdout":<text>} whenever that text differs from the last push (the
// first capture is always pushed); a capture failure is pushed once as
// {"event":"pane","session":"<id>","error":"..."}. Only one session is
// watched at a time: a new watch replaces the previous one (a watch for the
// session already watched restarts it, so the current screen is pushed
// again), {"id":3,"unwatch":true} stops it, and closing stdin stops it.
// Both are acknowledged with {"id":N,"code":0}.
// The local TUI uses this for the preview pane of the focused remote row
// instead of polling `session output --pane` over ssh.

type remoteAgentRequest struct {
	ID      int64    `json:"id"`
	Args    []string `json:"args,omitempty"`
	Watch   string   `json:"watch,omitempty"`
	Lines   int      `json:"lines,omitempty"`
	Unwatch bool     `json:"unwatch,omitempty"`
	// Cancel kills the subprocess of the request with this id, if any.
	Cancel bool `json:"cancel,omitempty"`
	// Ping is a liveness line from the peer; it resets the idle deadline.
	Ping bool `json:"ping,omitempty"`
}

type remoteAgentReply struct {
	ID     int64  `json:"id,omitempty"`
	Event  string `json:"event,omitempty"`
	Stdout string `json:"stdout,omitempty"`
	Stderr string `json:"stderr,omitempty"`
	Code   int    `json:"code"`
	Error  string `json:"error,omitempty"`
	// A "changed" event carries the fresh listings so the local side can
	// apply them at once instead of asking again.
	Sessions string `json:"sessions,omitempty"`
	Groups   string `json:"groups,omitempty"`
	// ProbeMS is how long the "changed" event's listings took to build.
	ProbeMS int64 `json:"probe_ms,omitempty"`
	// Stamp is the state DB's newest mtime (remote clock, unix nanoseconds)
	// when a "changed" event's listing was taken, or after a command ran.
	Stamp int64 `json:"stamp,omitempty"`
	// A "pane" event names the watched session; its text is in Stdout.
	Session string `json:"session,omitempty"`
}

// remoteAgentProbeFunc builds the current `list --json` and `group list
// --json` bodies for the change feed. The in-process one is the default;
// tests inject their own.
type remoteAgentProbeFunc func() (listJSON, groupJSON string, err error)

// remoteAgentRunFunc runs one CLI request and returns its stdout, stderr
// and exit code. ctx ends when the request is cancelled or times out.
type remoteAgentRunFunc func(ctx context.Context, args []string) (stdout, stderr string, code int)

// remoteAgentPaneEvery is how often the watched session's pane is captured.
// Pushes only go out when the text changed, so an idle pane costs one local
// capture per tick and nothing on the wire.
const remoteAgentPaneEvery = 300 * time.Millisecond

const (
	// remoteAgentStampEvery is how often the state DB's stamp is checked.
	remoteAgentStampEvery = 250 * time.Millisecond
	// remoteAgentProbeQuiet is the least time between two probes: a burst
	// of writes collapses into one probe after the last of them, and a
	// probe can never start again the moment it ends.
	remoteAgentProbeQuiet = 2 * time.Second
	// remoteAgentPingEvery is how often {"event":"ping"} is pushed.
	remoteAgentPingEvery = 20 * time.Second
	// remoteAgentIdleAfter is how long stdin may stay silent before the
	// agent decides its peer is gone and exits; the local side pings well
	// within it.
	remoteAgentIdleAfter = 10 * time.Minute
	// remoteAgentMaxConcurrent caps the subprocesses running at once.
	remoteAgentMaxConcurrent = 4
	// remoteAgentMaxPushBytes caps the listings a "changed" event carries;
	// above it the event goes bare and the local side fetches.
	remoteAgentMaxPushBytes = 4 << 20
	// remoteAgentWriteQueue is how many outbound lines may wait for the
	// pipe before the peer counts as gone.
	remoteAgentWriteQueue = 64
	// remoteAgentRequestTimeout bounds one subprocess.
	remoteAgentRequestTimeout = 5 * time.Minute
	// remoteAgentCloseWait is how long the exit waits for queued lines to
	// reach a stdout that may no longer be read.
	remoteAgentCloseWait = 2 * time.Second
	// remoteAgentWriteStall is how long a producer may wait for room in a
	// full outbound queue before the peer counts as gone.
	remoteAgentWriteStall = 30 * time.Second
	// remoteAgentDrainWait bounds how long a recycling agent waits for the
	// requests it already accepted before it exits.
	remoteAgentDrainWait = 30 * time.Second
)

// remoteAgentConfig wires serveRemoteAgent. Zero durations and counts take
// the defaults above; a nil probe or empty WatchPath disables the change
// feed, a nil Capture disables pane watching.
type remoteAgentConfig struct {
	Run        remoteAgentRunFunc
	Probe      remoteAgentProbeFunc
	WatchPath  string
	WatchEvery time.Duration
	ProbeQuiet time.Duration
	Capture    func(context.Context, string) (string, error)
	PaneEvery  time.Duration
	PingEvery  time.Duration
	IdleAfter  time.Duration
	// WriteStall bounds how long a write may wait for a full outbound queue.
	WriteStall time.Duration
	// MaxConcurrent caps subprocesses; MaxPushBytes caps pushed listings.
	MaxConcurrent int
	MaxPushBytes  int
	// BinaryWatch, when set, makes the agent exit cleanly once a newer
	// agent-deck is on disk and no request is in flight. The agent never
	// re-execs: the JSON-lines handshake would not survive it. The
	// controller (internal/session/remote_channel.go) sees EOF, marks the
	// channel down and redials on its next request, which starts the new
	// build; pane watches are re-asked after a reconnect. Idle and
	// Restart on the watcher are set by serveRemoteAgent.
	BinaryWatch *update.Watcher
}

func (c remoteAgentConfig) withDefaults() remoteAgentConfig {
	if c.ProbeQuiet <= 0 {
		c.ProbeQuiet = remoteAgentProbeQuiet
	}
	if c.PingEvery <= 0 {
		c.PingEvery = remoteAgentPingEvery
	}
	if c.IdleAfter <= 0 {
		c.IdleAfter = remoteAgentIdleAfter
	}
	if c.WriteStall <= 0 {
		c.WriteStall = remoteAgentWriteStall
	}
	if c.MaxConcurrent <= 0 {
		c.MaxConcurrent = remoteAgentMaxConcurrent
	}
	if c.MaxPushBytes <= 0 {
		c.MaxPushBytes = remoteAgentMaxPushBytes
	}
	return c
}

// remoteAgentDeniedVerbs are never run through the channel: they need a
// terminal, or must not be reachable from a remote TUI at all.
var remoteAgentDeniedVerbs = map[string]bool{
	"remote-agent": true, "web": true, "uninstall": true, "update": true,
}

func handleRemoteAgent(profile string, args []string) {
	for _, a := range args {
		if a == "--help" || a == "-h" {
			fmt.Println("Usage: agent-deck remote-agent")
			fmt.Println()
			fmt.Println("Serve JSON-line requests on stdin for a local agent-deck TUI (one persistent")
			fmt.Println("channel per remote) and push {\"event\":\"changed\"} when the profile's state changes.")
			fmt.Println("{\"id\":N,\"watch\":\"<session>\",\"lines\":200} follows one session's pane ({\"event\":\"pane\"} on change);")
			fmt.Println("{\"id\":N,\"unwatch\":true} stops it. {\"id\":N,\"cancel\":true} kills request N;")
			fmt.Println("{\"id\":N,\"ping\":true} keeps the channel alive. The agent pushes {\"event\":\"ping\"} itself")
			fmt.Println("and exits when stdout fails or stdin stays silent for 10 minutes.")
			return
		}
	}
	dbPath, err := session.GetDBPathForProfile(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "remote-agent: %v\n", err)
		os.Exit(1)
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "remote-agent: %v\n", err)
		os.Exit(1)
	}
	// The in-process probe refreshes statuses through tmux like `list` does,
	// and `list` fixes up PATH for tmux before it starts.
	ensureTmuxOnPath()
	// The probe must stay read-only (see newRemoteAgentProbe): this verb
	// is dispatched before main registers the global state DB, and it
	// must never be registered here.
	if statedb.GetGlobal() != nil {
		fmt.Fprintln(os.Stderr, "remote-agent: a global state DB is registered; the probe would write last-activity rows")
		os.Exit(1)
	}
	probe, closeProbe, err := newRemoteAgentProbe(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "remote-agent: %v\n", err)
		os.Exit(1)
	}
	defer closeProbe()
	runner := func(ctx context.Context, reqArgs []string) (string, string, int) {
		full := append([]string{"-p", profile}, reqArgs...)
		// The peer is the ssh-authenticated user who could run any of these
		// verbs as `ssh host agent-deck ...` anyway; the verb is checked
		// against the CLI's own registry and the deny list above, and the
		// arguments are passed as argv, never through a shell.
		cmd := exec.CommandContext(ctx, self, full...) //nolint:gosec // see comment above
		var out, errb strings.Builder
		cmd.Stdout = &out
		cmd.Stderr = &errb
		code := 0
		if err := cmd.Run(); err != nil {
			code = 1
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			}
		}
		return out.String(), errb.String(), code
	}
	serveRemoteAgent(context.Background(), os.Stdin, os.Stdout, remoteAgentConfig{
		Run:         runner,
		Probe:       probe,
		WatchPath:   dbPath,
		WatchEvery:  remoteAgentStampEvery,
		Capture:     remoteAgentPaneCapturer(profile),
		PaneEvery:   remoteAgentPaneEvery,
		BinaryWatch: newRemoteAgentBinaryWatch(self),
	})
}

// newRemoteAgentBinaryWatch returns the recycle-on-upgrade watcher for the
// agent, or nil when [updates].auto_restart is off on this host or update
// checks are disabled by environment.
func newRemoteAgentBinaryWatch(self string) *update.Watcher {
	if os.Getenv(update.SkipUpdateCheckEnv) != "" || !session.GetUpdateSettings().GetAutoRestart() {
		return nil
	}
	return &update.Watcher{
		Exe:            self,
		RunningVersion: Version,
		Log:            logging.ForComponent(logging.CompSession),
	}
}

// newRemoteAgentProbe opens the profile's storage once and returns a probe
// that loads it, refreshes statuses, and formats both listings exactly as
// the CLI would, plus the close for the storage handle. Like `list --json`
// it warms the tmux and hook-status caches once per probe, then formats
// both listings from that one load. The handle stays open for the life of
// the agent, so unlike a `list` subprocess no probe checkpoints the WAL.
//
// The probe is read-only. UpdateStatus flushes last-activity evidence
// through statedb.GetGlobal(), which a `list` subprocess registers at
// startup and this process never does (handleRemoteAgent checks), so the
// flush is a no-op here and a probe never moves the stamp the watcher
// compares. TestRemoteAgent_InProcessProbeMatchesCLI pins this.
func newRemoteAgentProbe(profile string) (remoteAgentProbeFunc, func(), error) {
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		return nil, nil, err
	}
	probe := func() (string, string, error) {
		instances, groups, err := storage.LoadWithGroups()
		if err != nil {
			return "", "", err
		}
		session.RefreshInstancesForCLIStatus(instances)
		l, err := buildListJSON(storage.Profile(), instances)
		if err != nil {
			return "", "", err
		}
		g, err := buildGroupListJSON(session.NewGroupTreeWithGroups(instances, groups))
		if err != nil {
			return "", "", err
		}
		return string(l), string(g), nil
	}
	return probe, func() { _ = storage.Close() }, nil
}

// remoteAgentPaneCapturer returns a capture func that reads the watched
// session's pane in-process: the session list is loaded once per watched
// session (and again, at most every few seconds, when the session cannot be
// captured, so a session that starts or restarts after the watch began is
// picked up), then every tick is one tmux capture, no subprocess of this
// binary and no DB read.
func remoteAgentPaneCapturer(profile string) func(context.Context, string) (string, error) {
	var (
		mu       sync.Mutex
		loadedID string
		loaded   *session.Instance
		loadedAt time.Time
	)
	const reloadAfter = 5 * time.Second
	find := func(sessionID string) (*session.Instance, error) {
		storage, instances, _, err := loadSessionData(profile)
		if err != nil {
			return nil, err
		}
		// The instances are fully in memory; this process lives as long as
		// the channel, so the DB handle must not be left open per reload.
		_ = storage.Close()
		for _, inst := range instances {
			if inst.ID == sessionID {
				return inst, nil
			}
		}
		return nil, fmt.Errorf("session '%s' not found", sessionID)
	}
	return func(_ context.Context, sessionID string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if loadedID != sessionID || (loaded == nil && time.Since(loadedAt) > reloadAfter) {
			loadedID, loadedAt = sessionID, time.Now()
			loaded, _ = find(sessionID)
		}
		if loaded == nil {
			return "", fmt.Errorf("session '%s' not found", sessionID)
		}
		content, err := loaded.PreviewFull()
		if err != nil && time.Since(loadedAt) > reloadAfter {
			// The pane may belong to a restarted session: reload once the
			// grace period is over instead of failing forever.
			loaded = nil
		}
		return content, err
	}
}

// remoteAgentWriter is the one path to stdout: a bounded queue drained by a
// single goroutine. write blocks for at most stall when the queue is full:
// a slow but live pipe (a large listing on a thin link while pane pushes
// keep coming) only backpressures the producers, as the old blocking write
// did; a pipe nobody reads any more, or one whose write fails, makes the
// peer count as gone and fail runs once (it ends the agent). After that
// every write returns at once so the shutdown never waits on the pipe.
type remoteAgentWriter struct {
	q        chan []byte
	done     chan struct{}
	failed   chan struct{}
	failOnce sync.Once
	fail     func()
	closed   sync.Once
	stall    time.Duration
}

func newRemoteAgentWriter(out io.Writer, size int, stall time.Duration, fail func()) *remoteAgentWriter {
	w := &remoteAgentWriter{q: make(chan []byte, size), done: make(chan struct{}), failed: make(chan struct{}), fail: fail, stall: stall}
	go func() {
		defer close(w.done)
		for b := range w.q {
			if _, err := out.Write(b); err != nil {
				w.giveUp()
				for range w.q { // drain until the producers stop
				}
				return
			}
		}
	}()
	return w
}

func (w *remoteAgentWriter) giveUp() {
	w.failOnce.Do(func() {
		close(w.failed)
		w.fail()
	})
}

func (w *remoteAgentWriter) write(r remoteAgentReply) {
	b, err := json.Marshal(r)
	if err != nil {
		return
	}
	line := append(b, '\n')
	select {
	case w.q <- line:
		return
	case <-w.failed:
		return
	default:
	}
	t := time.NewTimer(w.stall)
	defer t.Stop()
	select {
	case w.q <- line:
	case <-w.failed:
	case <-t.C:
		w.giveUp()
	}
}

// close ends the drain once every producer has stopped and waits, briefly,
// for the queued lines to reach the pipe. The wait is bounded: a peer that
// stopped reading leaves the drain stuck in a write that only ends when the
// pipe does, and the agent must still exit (the process ends the write).
func (w *remoteAgentWriter) close() {
	w.closed.Do(func() { close(w.q) })
	select {
	case <-w.done:
	case <-time.After(remoteAgentCloseWait):
	}
}

// serveRemoteAgent is the agent loop, separated from process wiring so it is
// testable with pipes. It returns when stdin closes, when stdout stops
// taking lines, or when stdin stays silent past the idle deadline.
func serveRemoteAgent(ctx context.Context, in io.Reader, out io.Writer, cfg remoteAgentConfig) {
	cfg = cfg.withDefaults()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := newRemoteAgentWriter(out, remoteAgentWriteQueue, cfg.WriteStall, cancel)
	write := w.write

	write(remoteAgentReply{Event: "ready"})

	var wg sync.WaitGroup
	if cfg.WatchPath != "" && cfg.WatchEvery > 0 && cfg.Probe != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			watchRemoteState(ctx, cfg, write)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(cfg.PingEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				write(remoteAgentReply{Event: "ping"})
			}
		}
	}()

	// Pane watch: one goroutine at a time, replaced by the next watch
	// (every watch request starts afresh, so the peer always gets the
	// current screen back even when it re-asks for the same session) and
	// stopped by unwatch or by stdin closing (ctx).
	var (
		watchMu   sync.Mutex
		stopWatch context.CancelFunc
		watchDone chan struct{}
	)
	setWatch := func(sessionID string, lines int) {
		watchMu.Lock()
		defer watchMu.Unlock()
		if stopWatch != nil {
			stopWatch()
			<-watchDone
			stopWatch, watchDone = nil, nil
		}
		if sessionID == "" {
			return
		}
		wctx, wcancel := context.WithCancel(ctx)
		done := make(chan struct{})
		stopWatch, watchDone = wcancel, done
		go func() {
			defer close(done)
			watchRemotePane(wctx, sessionID, lines, cfg.Capture, cfg.PaneEvery, write)
		}()
	}

	requests := newRemoteAgentRequests(cfg.Run, cfg.MaxConcurrent, cfg.WatchPath)

	// Recycle on upgrade: the watcher's "restart" only closes recycle; the
	// loop below stops reading, drains accepted requests and exits so the
	// controller redials into the new build.
	recycle := make(chan struct{})
	if w := cfg.BinaryWatch; w != nil {
		var once sync.Once
		w.Idle = func() bool { return requests.inFlight() == 0 }
		w.Restart = func(string) error {
			once.Do(func() { close(recycle) })
			return nil
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.Run(ctx)
		}()
	}

	// stdin is read on its own goroutine so the loop can also leave on a
	// dead stdout or an idle deadline while a read is blocked.
	lines := make(chan string)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(in)
		sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	idle := time.NewTimer(cfg.IdleAfter)
	defer idle.Stop()
	draining := false
loop:
	for {
		var (
			raw string
			ok  bool
		)
		select {
		case <-ctx.Done():
			break loop
		case <-idle.C:
			fmt.Fprintf(os.Stderr, "remote-agent: no input for %s, exiting\n", cfg.IdleAfter)
			break loop
		case <-recycle:
			fmt.Fprintf(os.Stderr, "remote-agent: binary upgraded (running v%s); exiting so the controller reconnects\n", Version)
			draining = true
			break loop
		case raw, ok = <-lines:
			if !ok {
				break loop
			}
		}
		if !idle.Stop() {
			select {
			case <-idle.C:
			default:
			}
		}
		idle.Reset(cfg.IdleAfter)
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		var req remoteAgentRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			write(remoteAgentReply{Code: 2, Error: "bad request: " + err.Error()})
			continue
		}
		switch {
		case req.Cancel:
			requests.cancel(req.ID)
			continue
		case req.Ping:
			if req.ID != 0 {
				write(remoteAgentReply{ID: req.ID})
			}
			continue
		case req.Watch != "" || req.Unwatch:
			switch {
			case cfg.Capture == nil:
				write(remoteAgentReply{ID: req.ID, Code: 2, Error: "pane watch not available"})
			case req.Unwatch:
				setWatch("", 0)
				write(remoteAgentReply{ID: req.ID})
			default:
				// Acknowledge first so the first pane push always follows
				// the ack on the wire.
				write(remoteAgentReply{ID: req.ID})
				setWatch(req.Watch, req.Lines)
			}
			continue
		}
		if !remoteAgentArgsAllowed(req.Args) {
			write(remoteAgentReply{ID: req.ID, Code: 2, Error: "verb not allowed over the channel"})
			continue
		}
		// Register before handing off so a cancel line that follows the
		// request line on stdin always finds it.
		f := requests.start(ctx, req)
		wg.Add(1)
		go func(req remoteAgentRequest, f *remoteAgentFlight) {
			defer wg.Done()
			if reply, ok := requests.wait(req, f); ok {
				write(reply)
			}
		}(req, f)
	}
	if draining {
		// A request that slipped in between the idle check and the recycle
		// still gets its reply; only then is the context cancelled.
		requests.drain(remoteAgentDrainWait)
	}
	cancel()
	setWatch("", 0)
	wg.Wait()
	w.close()
}

// watchRemoteState is the change feed. A cheap mtime/size poll on state.db
// notices any write by any process on the remote; the stamp only triggers a
// probe, and the agent pushes "changed" only when the two listings differ
// from what it last pushed. Probes are debounced: one runs no sooner than
// ProbeQuiet after the previous one ended, so a burst of writes costs one
// probe, and a write landing while a probe runs (which that probe may or
// may not have seen) is probed again rather than taken as seen. The probe
// is read-only, so a stamp that moved during it was moved by someone else.
func watchRemoteState(ctx context.Context, cfg remoteAgentConfig, write func(remoteAgentReply)) {
	last := remoteAgentStamp(cfg.WatchPath)
	// Seed with the current content so the first stamp change is judged
	// against what the TUI already fetched at startup.
	l0, g0, _ := cfg.Probe()
	lastHash := remoteAgentContentHash(l0, g0)
	lastProbeEnd := time.Now()
	pending := false
	t := time.NewTicker(cfg.WatchEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if s := remoteAgentStamp(cfg.WatchPath); s != last {
			last, pending = s, true
		}
		if !pending || time.Since(lastProbeEnd) < cfg.ProbeQuiet {
			continue
		}
		pending = false
		before := last
		stamp := remoteAgentStampNanos(cfg.WatchPath)
		started := time.Now()
		l, g, err := cfg.Probe()
		elapsed := time.Since(started).Milliseconds()
		lastProbeEnd = time.Now()
		if ctx.Err() != nil {
			return
		}
		switch {
		case err != nil:
			// No listings to compare or push: tell the TUI something
			// changed and let it fetch.
			fmt.Fprintf(os.Stderr, "remote-agent: probe: %v\n", err)
			write(remoteAgentReply{Event: "changed", ProbeMS: elapsed, Stamp: stamp})
		case !remoteAgentListingsLookValid(l, g):
			fmt.Fprintf(os.Stderr, "remote-agent: probe produced no listing (%s)\n", remoteAgentHead(l))
			write(remoteAgentReply{Event: "changed", ProbeMS: elapsed, Stamp: stamp})
		default:
			if h := remoteAgentContentHash(l, g); h != lastHash {
				lastHash = h
				cl, cg, fits := remoteAgentCompactListings(l, g, cfg.MaxPushBytes)
				if fits {
					write(remoteAgentReply{Event: "changed", Sessions: cl, Groups: cg, ProbeMS: elapsed, Stamp: stamp})
				} else {
					write(remoteAgentReply{Event: "changed", ProbeMS: elapsed, Stamp: stamp})
				}
			}
		}
		// A write that landed while the probe ran may or may not be in
		// the listing: probe again after the quiet interval.
		if after := remoteAgentStamp(cfg.WatchPath); after != before {
			last, pending = after, true
		}
	}
}

// remoteAgentListingsLookValid is the shape check a pushed listing must
// pass: an array for sessions and an object for groups. A listing that is
// an error message (say "Error: database is locked") fails it and the event
// goes bare, so the local side fetches instead of applying an empty fleet.
func remoteAgentListingsLookValid(listJSON, groupJSON string) bool {
	l, g := strings.TrimSpace(listJSON), strings.TrimSpace(groupJSON)
	return strings.HasPrefix(l, "[") && strings.HasPrefix(g, "{")
}

func remoteAgentHead(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 60 {
		s = s[:60] + "..."
	}
	return s
}

// remoteAgentCompactListings strips the indentation the CLI prints so the
// event carries the same JSON documents in json.Marshal form. A listing
// that is not valid JSON is kept as it is (the local parser decides). fits
// is false when the compact pair would exceed maxBytes.
func remoteAgentCompactListings(listJSON, groupJSON string, maxBytes int) (string, string, bool) {
	compact := func(s string) string {
		var b bytes.Buffer
		if err := json.Compact(&b, []byte(s)); err != nil {
			return s
		}
		return b.String()
	}
	cl, cg := compact(listJSON), compact(groupJSON)
	return cl, cg, len(cl)+len(cg) <= maxBytes
}

// remoteAgentRequests runs command requests: a semaphore caps the
// subprocesses, identical read-only requests in flight share one run, and
// a request can be cancelled by id.
type remoteAgentRequests struct {
	exec      remoteAgentRunFunc
	sem       chan struct{}
	stampPath string
	mu        sync.Mutex
	byID      map[int64]*remoteAgentFlight
	byKey     map[string]*remoteAgentFlight
}

type remoteAgentFlight struct {
	key     string
	cancel  context.CancelFunc
	done    chan struct{}
	waiters int
	stdout  string
	stderr  string
	code    int
	stamp   int64
}

func newRemoteAgentRequests(run remoteAgentRunFunc, maxConcurrent int, stampPath string) *remoteAgentRequests {
	return &remoteAgentRequests{
		exec:      run,
		sem:       make(chan struct{}, maxConcurrent),
		stampPath: stampPath,
		byID:      map[int64]*remoteAgentFlight{},
		byKey:     map[string]*remoteAgentFlight{},
	}
}

// remoteAgentReadOnlyVerbs lists the requests that only read state, so two
// identical ones in flight can share one subprocess.
func remoteAgentReadOnly(args []string) bool {
	switch args[0] {
	case "list", "ls", "costs", "status", "version":
		return true
	case "group":
		return len(args) > 1 && args[1] == "list"
	case "session":
		return len(args) > 1 && (args[1] == "output" || args[1] == "status")
	}
	return false
}

// start registers req and begins its run (or joins an identical read-only
// run in flight). It returns at once; wait collects the reply.
func (r *remoteAgentRequests) start(ctx context.Context, req remoteAgentRequest) *remoteAgentFlight {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := ""
	if remoteAgentReadOnly(req.Args) {
		key = strings.Join(req.Args, "\x00")
	}
	f := r.byKey[key]
	if key == "" || f == nil {
		fctx, fcancel := context.WithTimeout(ctx, remoteAgentRequestTimeout)
		f = &remoteAgentFlight{key: key, cancel: fcancel, done: make(chan struct{})}
		if key != "" {
			r.byKey[key] = f
		}
		go r.execute(fctx, f, req.Args)
	}
	f.waiters++
	r.byID[req.ID] = f
	return f
}

// wait blocks until req's run ends and returns its reply; ok is false when
// the request was cancelled meanwhile, in which case nothing is answered.
func (r *remoteAgentRequests) wait(req remoteAgentRequest, f *remoteAgentFlight) (remoteAgentReply, bool) {
	<-f.done
	r.mu.Lock()
	cancelled := r.byID[req.ID] != f
	delete(r.byID, req.ID)
	r.mu.Unlock()
	if cancelled {
		return remoteAgentReply{}, false
	}
	return remoteAgentReply{ID: req.ID, Stdout: f.stdout, Stderr: f.stderr, Code: f.code, Stamp: f.stamp}, true
}

func (r *remoteAgentRequests) execute(ctx context.Context, f *remoteAgentFlight, args []string) {
	defer close(f.done)
	defer f.cancel()
	defer func() {
		r.mu.Lock()
		if f.key != "" && r.byKey[f.key] == f {
			delete(r.byKey, f.key)
		}
		r.mu.Unlock()
	}()
	select {
	case r.sem <- struct{}{}:
	case <-ctx.Done():
		f.stderr, f.code = "cancelled while queued", 1
		return
	}
	defer func() { <-r.sem }()
	f.stdout, f.stderr, f.code = r.exec(ctx, args)
	f.stamp = remoteAgentStampNanos(r.stampPath)
}

// inFlight is the number of requests registered and not yet answered.
func (r *remoteAgentRequests) inFlight() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byID)
}

// drain waits until no request is in flight or timeout has passed.
func (r *remoteAgentRequests) drain(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for r.inFlight() > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
}

// cancel drops request id: its subprocess is killed once no other request
// shares the run, and no reply is written for it.
func (r *remoteAgentRequests) cancel(id int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.byID[id]
	if !ok {
		return
	}
	delete(r.byID, id)
	f.waiters--
	if f.waiters <= 0 {
		if f.key != "" && r.byKey[f.key] == f {
			delete(r.byKey, f.key)
		}
		f.cancel()
	}
}

// watchRemotePane captures sessionID's pane every paneEvery until ctx ends
// and pushes a "pane" event when the text (or the failure) differs from the
// last push. Only the last lines lines are kept (all when lines <= 0): the
// capture is up to 2000 lines of scrollback and a busy pane changes on
// every tick, so the peer says how much of it it renders. The first capture
// is always pushed so the watcher starts with the current screen.
func watchRemotePane(ctx context.Context, sessionID string, lines int, capture func(context.Context, string) (string, error), paneEvery time.Duration, write func(remoteAgentReply)) {
	var lastHash string
	first := true
	t := time.NewTicker(paneEvery)
	defer t.Stop()
	for {
		content, err := capture(ctx, sessionID)
		if ctx.Err() != nil {
			return
		}
		reply := remoteAgentReply{Event: "pane", Session: sessionID, Stdout: tailLines(content, lines)}
		if err != nil {
			reply = remoteAgentReply{Event: "pane", Session: sessionID, Error: err.Error()}
		}
		sum := sha256.Sum256([]byte(reply.Error + "\x00" + reply.Stdout))
		h := hex.EncodeToString(sum[:8])
		if first || h != lastHash {
			first = false
			lastHash = h
			write(reply)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// tailLines keeps the last n lines of s (all of s when n <= 0).
func tailLines(s string, n int) string {
	if n <= 0 || s == "" {
		return s
	}
	end := len(s)
	if strings.HasSuffix(s, "\n") {
		end--
	}
	for i := 0; i < n; i++ {
		cut := strings.LastIndexByte(s[:end], '\n')
		if cut < 0 {
			return s
		}
		end = cut
	}
	return s[end+1:]
}

// remoteAgentArgsAllowed admits only a known CLI verb that is not on the
// deny list, with arguments that cannot break the line protocol.
func remoteAgentArgsAllowed(args []string) bool {
	if len(args) == 0 || remoteAgentDeniedVerbs[args[0]] || !commandRegistry[args[0]] {
		return false
	}
	for _, a := range args {
		if strings.ContainsAny(a, "\n\r") {
			return false
		}
	}
	return true
}

// remoteAgentContentHash hashes what the TUI would see, ignoring the
// last_activity timestamps that a status refresh rewrites on every listing.
func remoteAgentContentHash(listJSON, groupJSON string) string {
	scrub := remoteAgentActivityField.ReplaceAllString(listJSON, "")
	sum := sha256.Sum256([]byte(scrub + "\x00" + groupJSON))
	return hex.EncodeToString(sum[:8])
}

var remoteAgentActivityField = regexp.MustCompile(`"last_activity_at":\s*"[^"]*",?`)

// remoteAgentStamp folds the mtime and size of the state DB, its WAL and
// SHM into one comparable string.
func remoteAgentStamp(dbPath string) string {
	var b strings.Builder
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if st, err := os.Stat(p); err == nil {
			fmt.Fprintf(&b, "%s:%d:%d;", filepath.Base(p), st.ModTime().UnixNano(), st.Size())
		}
	}
	return b.String()
}

// remoteAgentStampNanos is the newest mtime among the state DB, its WAL and
// SHM in unix nanoseconds: an ordering key on the remote's clock for when a
// listing was taken or a command finished. 0 when nothing can be stat'ed.
func remoteAgentStampNanos(dbPath string) int64 {
	var newest int64
	if dbPath == "" {
		return 0
	}
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if st, err := os.Stat(p); err == nil && st.ModTime().UnixNano() > newest {
			newest = st.ModTime().UnixNano()
		}
	}
	return newest
}
