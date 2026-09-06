package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// RemoteChannel is the local end of one persistent ssh session per remote
// running `agent-deck remote-agent` (#2174). Commands go over it as JSON
// lines and come back with their id; the remote pushes {"event":"changed"}
// whenever its state DB changes, which the TUI turns into an immediate
// refetch. When the channel is down (remote too old, ssh dropped), callers
// fall back to one ssh exec per command exactly as before.
type RemoteChannel struct {
	name string
	// identity is host, profile and binary path of the remote this channel
	// was dialled for. A config edit that re-points the name at another host
	// gets a fresh channel instead of requests to the old one.
	identity string
	dial     func(ctx context.Context) (io.WriteCloser, io.Reader, func(), error)
	events   *remoteChangeMailbox
	mu       sync.Mutex
	stdin    io.WriteCloser
	closeFn  func()
	pending  map[int64]chan remoteChannelReply
	nextID   atomic.Int64
	up       atomic.Bool
	// gen counts transports. markDown only tears down the transport it was
	// called for, so a reader or ping loop of an old transport can never
	// kill the one dialled after it.
	gen uint64
	// unsupportedUntil is set when the remote does not know remote-agent
	// (old build): no reconnect attempts until then.
	unsupportedUntil time.Time
	lastAttempt      time.Time
	backoff          time.Duration
	dialing          bool
	// closed is set by Close: no redial, no more pushes.
	closed bool
	// watching is the session whose pane the remote agent is pushing for
	// this channel ("" for none); reset when the transport drops, because
	// the agent's watch dies with it. watchUnsupported is set when the
	// agent answered a watch request with an error (a build that predates
	// pane watching): the caller then polls the preview as before.
	watching         string
	watchUnsupported bool
	// timeouts counts requests in a row that got no reply before their
	// deadline; lastRead is when the agent last said anything. Together
	// they detect a half-open link (#5): writes into a dead ssh session
	// still succeed, so only silence gives it away.
	timeouts int
	lastRead time.Time
	// Tunables, zero for the defaults below (tests shrink them).
	pingEvery    time.Duration
	pingTimeout  time.Duration
	helloTimeout time.Duration
	maxFrame     int
}

const (
	// remoteChannelPingEvery is how often the client pings an idle channel;
	// remoteChannelPingTimeout is how long it waits for the answer before
	// declaring the link half-open.
	remoteChannelPingEvery   = 20 * time.Second
	remoteChannelPingTimeout = 20 * time.Second
	// remoteChannelHelloTimeout bounds the wait for the agent's ready line.
	remoteChannelHelloTimeout = 15 * time.Second
	// remoteChannelMaxFrame caps one line from the agent (#13). A 2000
	// session listing is about 2 MB; anything near this limit is a verb
	// gone wrong, and it costs a reconnect rather than the TUI's memory.
	remoteChannelMaxFrame = 32 << 20
	// remoteChannelTimeoutsToDrop is how many consecutive request timeouts
	// mark the transport down.
	remoteChannelTimeoutsToDrop = 2
	// remoteChannelUnsupportedFor is how long an old remote (no
	// remote-agent verb) is left alone before the channel is tried again.
	remoteChannelUnsupportedFor = 10 * time.Minute
)

type remoteChannelRequest struct {
	ID      int64    `json:"id"`
	Args    []string `json:"args,omitempty"`
	Watch   string   `json:"watch,omitempty"`
	Lines   int      `json:"lines,omitempty"`
	Unwatch bool     `json:"unwatch,omitempty"`
	// Ping asks for any reply at all. An agent that predates pings answers
	// it with a "verb not allowed" error, which proves the link just as
	// well.
	Ping bool `json:"ping,omitempty"`
}

type remoteChannelReply struct {
	ID       int64  `json:"id,omitempty"`
	Event    string `json:"event,omitempty"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	Code     int    `json:"code"`
	Error    string `json:"error,omitempty"`
	Sessions string `json:"sessions,omitempty"`
	Groups   string `json:"groups,omitempty"`
	ProbeMS  int64  `json:"probe_ms,omitempty"`
	Session  string `json:"session,omitempty"`
}

// RemoteChange is one pushed event from a remote. For a "changed" event,
// Sessions and Groups are the remote's fresh listings when the agent sent
// them (nil when it did not, in which case the receiver fetches). For a
// "pane" event, Pane is set and the listing fields are empty.
type RemoteChange struct {
	Remote   string
	Sessions []RemoteSessionInfo
	Groups   []string
	HasData  bool
	Pane     *RemotePaneEvent
}

// RemotePaneEvent is one pushed pane capture for the watched session: the
// pane text after a change, or the capture failure (Err) when the pane
// could not be read.
type RemotePaneEvent struct {
	Session string
	Content string
	Err     string
}

// errChannelDown means the transport failed before the request reached the
// agent; callers fall back to a plain ssh exec.
var errChannelDown = errors.New("remote channel down")

// errChannelInterrupted means the request was written to the agent and the
// transport failed before its reply arrived. The remote may have executed
// the command (#3), so callers must not run it again blindly: read-only
// verbs may be retried, anything else is reported so the caller refetches.
var errChannelInterrupted = errors.New("remote channel interrupted before the reply")

// errChannelFrameTooLarge is the reader's verdict on a line longer than
// maxFrame; it takes the transport down instead of the process (#13).
var errChannelFrameTooLarge = errors.New("remote channel frame too large")

// isChannelTransportErr reports whether err is one of the channel's own
// transport failures rather than a verdict from the remote command.
func isChannelTransportErr(err error) bool {
	return errors.Is(err, errChannelDown) || errors.Is(err, errChannelInterrupted)
}

// remoteChangeMailbox is the fan-in for pushed events (#14). It keeps the
// newest "changed" snapshot per remote and the newest pane capture per
// watched session; a burst from one remote collapses into one delivery of
// its latest state, and one remote's burst never delays another's.
//
// Ordering guarantee: per slot (a remote's listing, or one session's pane)
// deliveries are in arrival order and never go backwards; intermediate
// snapshots may be skipped. A "changed" push for one remote is delivered
// independently of its pane pushes.
type remoteChangeMailbox struct {
	mu     sync.Mutex
	slots  map[string]RemoteChange
	queue  []string
	notify chan struct{}
	out    chan RemoteChange
	once   sync.Once
}

func newRemoteChangeMailbox() *remoteChangeMailbox {
	return &remoteChangeMailbox{
		slots:  map[string]RemoteChange{},
		notify: make(chan struct{}, 1),
		out:    make(chan RemoteChange),
	}
}

func mailboxKey(ch RemoteChange) string {
	if ch.Pane != nil {
		return "pane\x00" + ch.Remote + "\x00" + ch.Pane.Session
	}
	return "changed\x00" + ch.Remote
}

// put stores ch as the newest state of its slot. A slot already waiting
// keeps its place in the queue and only its content is replaced.
func (m *remoteChangeMailbox) put(ch RemoteChange) {
	key := mailboxKey(ch)
	m.mu.Lock()
	if _, waiting := m.slots[key]; !waiting {
		m.queue = append(m.queue, key)
	}
	m.slots[key] = ch
	m.mu.Unlock()
	select {
	case m.notify <- struct{}{}:
	default:
	}
}

// drop discards everything waiting for one remote (its channel closed).
func (m *remoteChangeMailbox) drop(remote string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.queue[:0]
	for _, key := range m.queue {
		if m.slots[key].Remote == remote {
			delete(m.slots, key)
			continue
		}
		kept = append(kept, key)
	}
	m.queue = kept
}

// Events returns the delivery channel, starting the pump on first use.
func (m *remoteChangeMailbox) Events() <-chan RemoteChange {
	m.once.Do(func() { go m.pump() })
	return m.out
}

// pump hands the oldest waiting slot to the receiver. Nothing is taken out
// of the mailbox until the receiver is about to get it, so a newer push
// during a slow receive is still what arrives next.
func (m *remoteChangeMailbox) pump() {
	for range m.notify {
		for {
			m.mu.Lock()
			if len(m.queue) == 0 {
				m.mu.Unlock()
				break
			}
			key := m.queue[0]
			m.queue = m.queue[1:]
			ch := m.slots[key]
			delete(m.slots, key)
			m.mu.Unlock()
			m.out <- ch
		}
	}
}

// remoteChangeInbox fans in pushes from every remote for the TUI.
var remoteChangeInbox = newRemoteChangeMailbox()

// RemoteChangeEvents delivers pushed changes: the latest listing per remote
// and the latest pane capture per watched session, in arrival order per
// slot (see remoteChangeMailbox for the guarantee).
func RemoteChangeEvents() <-chan RemoteChange { return remoteChangeInbox.Events() }

var (
	remoteChannelsMu sync.Mutex
	remoteChannels   = map[string]*RemoteChannel{}
)

// RemoteChannelFor returns the channel already opened for a named remote,
// or nil when none has been started yet (the first command to that remote
// starts it). It never dials.
func RemoteChannelFor(name string) *RemoteChannel {
	remoteChannelsMu.Lock()
	defer remoteChannelsMu.Unlock()
	return remoteChannels[name]
}

// remoteChannelsEnabled lets AGENT_DECK_REMOTE_CHANNEL=0 turn the channel off
// (every command then runs as its own ssh exec, the pre-#2174 behaviour).
func remoteChannelsEnabled() bool {
	v := strings.TrimSpace(os.Getenv("AGENT_DECK_REMOTE_CHANNEL"))
	return v == "" || v == "1" || strings.EqualFold(v, "true")
}

// remoteIdentity is the part of a remote's config a channel is bound to.
func remoteIdentity(host, profile, path string) string {
	return host + "\x00" + profile + "\x00" + path
}

// channelFor returns the shared channel for a runner's remote, starting it
// on first use. nil when channels are disabled or the runner is unnamed. A
// channel dialled for another host, profile or binary under the same name
// is closed and replaced (#8).
func channelFor(r *SSHRunner) *RemoteChannel {
	if r == nil || r.name == "" || r.runFn != nil || !remoteChannelsEnabled() {
		return nil
	}
	identity := remoteIdentity(r.Host, r.Profile, r.AgentDeckPath)
	remoteChannelsMu.Lock()
	defer remoteChannelsMu.Unlock()
	if ch, ok := remoteChannels[r.name]; ok {
		if ch.identity == identity {
			if !ch.up.Load() {
				// A dropped transport is redialled by the next request to
				// that remote (ensureConnected is a no-op while a dial is
				// running, during backoff or after Close).
				go ch.ensureConnected()
			}
			return ch
		}
		ch.Close()
		delete(remoteChannels, r.name)
	}
	rc := *r
	dial := rc.dialChannelFn
	if dial == nil {
		dial = func(ctx context.Context) (io.WriteCloser, io.Reader, func(), error) {
			return rc.dialRemoteAgent(ctx)
		}
	}
	ch := newRemoteChannel(r.name, identity, dial)
	remoteChannels[r.name] = ch
	go ch.ensureConnected()
	return ch
}

func newRemoteChannel(name, identity string, dial func(ctx context.Context) (io.WriteCloser, io.Reader, func(), error)) *RemoteChannel {
	return &RemoteChannel{
		name:     name,
		identity: identity,
		events:   remoteChangeInbox,
		pending:  map[int64]chan remoteChannelReply{},
		backoff:  2 * time.Second,
		dial:     dial,
	}
}

// ReconcileRemoteChannels closes the channels of remotes that are no longer
// configured, or whose host, profile or binary path changed, so a removed
// remote stops pushing rows into the tree and a re-pointed name redials
// (#8). Call it whenever the remote config is (re)loaded, before the fetch
// round that uses it.
func ReconcileRemoteChannels(config map[string]RemoteConfig) {
	remoteChannelsMu.Lock()
	defer remoteChannelsMu.Unlock()
	for name, ch := range remoteChannels {
		rc, ok := config[name]
		if ok && ch.identity == remoteIdentity(rc.Host, rc.GetProfile(), rc.GetAgentDeckPath()) {
			continue
		}
		ch.Close()
		delete(remoteChannels, name)
	}
}

// CloseRemoteChannels closes every channel (TUI shutdown), which ends each
// remote's agent process instead of leaving it to sshd's keepalive.
func CloseRemoteChannels() {
	remoteChannelsMu.Lock()
	defer remoteChannelsMu.Unlock()
	for name, ch := range remoteChannels {
		ch.Close()
		delete(remoteChannels, name)
	}
}

// dialRemoteAgent starts `ssh host agent-deck -p profile remote-agent` with
// pipes on both ends, over the same ControlMaster socket every other command
// uses.
func (r *SSHRunner) dialRemoteAgent(ctx context.Context) (io.WriteCloser, io.Reader, func(), error) {
	if err := ValidateSSHHost(r.Host); err != nil {
		return nil, nil, nil, err
	}
	_ = os.MkdirAll(sshControlDir, 0700)
	// A stale ControlMaster socket would hang this dial forever (#1421),
	// which the hello timeout would then read as a slow remote.
	CleanStaleSSHSockets()
	// Same argv construction as every other ssh exec in this file (host
	// validated above, options fixed, remote command shell-quoted).
	cmd := exec.CommandContext(ctx, "ssh", r.sshChannelArgs(r.buildRemoteCommand("remote-agent"))...) //nolint:gosec // see comment above
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, err
	}
	closeFn := func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}
	return stdin, stdout, closeFn, nil
}

// Connected reports whether requests can go over the channel right now.
func (c *RemoteChannel) Connected() bool { return c.up.Load() }

// Close takes the channel down for good: the transport is torn down, requests
// in flight fail, nothing waiting in the mailbox for this remote is
// delivered, and there is no redial.
func (c *RemoteChannel) Close() {
	c.mu.Lock()
	c.closed = true
	gen := c.gen
	c.mu.Unlock()
	c.markDownGen(gen)
	c.events.drop(c.name)
}

func (c *RemoteChannel) tunables() (pingEvery, pingTimeout, helloTimeout time.Duration, maxFrame int) {
	pingEvery, pingTimeout, helloTimeout, maxFrame = c.pingEvery, c.pingTimeout, c.helloTimeout, c.maxFrame
	if pingEvery <= 0 {
		pingEvery = remoteChannelPingEvery
	}
	if pingTimeout <= 0 {
		pingTimeout = remoteChannelPingTimeout
	}
	if helloTimeout <= 0 {
		helloTimeout = remoteChannelHelloTimeout
	}
	if maxFrame <= 0 {
		maxFrame = remoteChannelMaxFrame
	}
	return pingEvery, pingTimeout, helloTimeout, maxFrame
}

// ensureConnected dials if the channel is down and a retry is due. It never
// blocks a caller: connecting happens on its own goroutine and requests made
// meanwhile fall back to ssh exec.
func (c *RemoteChannel) ensureConnected() {
	c.mu.Lock()
	if c.closed || c.up.Load() || c.dialing || time.Now().Before(c.unsupportedUntil) || time.Since(c.lastAttempt) < c.backoff {
		c.mu.Unlock()
		return
	}
	c.dialing = true
	c.lastAttempt = time.Now()
	c.mu.Unlock()

	retryLater := func() {
		c.mu.Lock()
		c.dialing = false
		c.backoff = minDuration(c.backoff*2, 30*time.Second)
		c.mu.Unlock()
	}
	ctx, cancel := context.WithCancel(context.Background())
	stdin, stdout, closeFn, err := c.dial(ctx)
	if err != nil {
		cancel()
		retryLater()
		return
	}
	// The agent announces itself. Only readable text that is not JSON (an
	// old build's usage or unknown-command output) means the remote has no
	// remote-agent and is left alone for a while (#7); EOF, a timeout or a
	// stale socket are link trouble and get the short backoff.
	_, _, helloTimeout, maxFrame := c.tunables()
	reader := bufio.NewReader(stdout)
	firstLine, rerr := readLineWithin(reader, helloTimeout, maxFrame)
	var hello remoteChannelReply
	switch {
	case rerr != nil:
		closeFn()
		cancel()
		retryLater()
		return
	case json.Unmarshal([]byte(firstLine), &hello) != nil || hello.Event != "ready":
		closeFn()
		cancel()
		c.mu.Lock()
		c.dialing = false
		c.unsupportedUntil = time.Now().Add(remoteChannelUnsupportedFor)
		c.mu.Unlock()
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		closeFn()
		cancel()
		return
	}
	c.gen++
	gen := c.gen
	c.stdin = stdin
	c.closeFn = func() { closeFn(); cancel() }
	c.dialing = false
	c.backoff = 2 * time.Second
	c.timeouts = 0
	c.lastRead = time.Now()
	c.up.Store(true)
	c.mu.Unlock()
	go c.readLoop(reader, gen, maxFrame)
	go c.pingLoop(ctx, gen)
}

// readFrame reads one newline-terminated line, refusing to buffer more than
// max bytes of it.
func readFrame(r *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(line)+len(chunk) > max {
			return nil, errChannelFrameTooLarge
		}
		line = append(line, chunk...)
		if err == nil {
			return line, nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, err
		}
	}
}

func readLineWithin(r *bufio.Reader, d time.Duration, maxFrame int) (string, error) {
	type res struct {
		s   string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		s, err := readFrame(r, maxFrame)
		ch <- res{string(s), err}
	}()
	select {
	case v := <-ch:
		return strings.TrimSpace(v.s), v.err
	case <-time.After(d):
		return "", errors.New("timeout waiting for remote-agent")
	}
}

// pingLoop keeps a quiet channel honest (#5): after pingEvery of silence it
// sends a ping and takes the transport down when no reply comes within
// pingTimeout. Any traffic from the agent, including its own ping events,
// counts as life and postpones the next ping.
func (c *RemoteChannel) pingLoop(ctx context.Context, gen uint64) {
	pingEvery, pingTimeout, _, _ := c.tunables()
	t := time.NewTicker(pingEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c.mu.Lock()
		quiet := time.Since(c.lastRead) >= pingEvery
		live := c.gen == gen && !c.closed
		c.mu.Unlock()
		if !live {
			return
		}
		if !quiet {
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, pingTimeout)
		_, err := c.roundTrip(pctx, remoteChannelRequest{Ping: true})
		cancel()
		if errors.Is(err, context.DeadlineExceeded) {
			sessionLog.Debug("remote_channel_ping_lost", slog.String("remote", c.name))
			c.markDownGen(gen)
			return
		}
	}
}

func (c *RemoteChannel) readLoop(r *bufio.Reader, gen uint64, maxFrame int) {
	for {
		line, err := readFrame(r, maxFrame)
		if err != nil {
			if errors.Is(err, errChannelFrameTooLarge) {
				sessionLog.Warn("remote_channel_frame_too_large", slog.String("remote", c.name), slog.Int("limit", maxFrame))
			}
			c.markDownGen(gen)
			return
		}
		c.mu.Lock()
		c.lastRead = time.Now()
		c.timeouts = 0
		c.mu.Unlock()
		var reply remoteChannelReply
		if json.Unmarshal([]byte(strings.TrimSpace(string(line))), &reply) != nil {
			continue
		}
		if reply.Event == "changed" {
			sessionLog.Debug("remote_channel_changed",
				slog.String("remote", c.name),
				slog.Bool("pushed_data", strings.TrimSpace(reply.Sessions) != ""),
				slog.Int64("probe_ms", reply.ProbeMS))
			c.publish(c.changeFromReply(reply))
			continue
		}
		if reply.Event == "pane" {
			c.publish(RemoteChange{Remote: c.name, Pane: &RemotePaneEvent{Session: reply.Session, Content: reply.Stdout, Err: reply.Error}})
			continue
		}
		if reply.ID == 0 {
			// Other events (an agent's own ping) only count as life.
			continue
		}
		c.mu.Lock()
		ch, ok := c.pending[reply.ID]
		delete(c.pending, reply.ID)
		c.mu.Unlock()
		if ok {
			ch <- reply
		}
	}
}

// publish hands an event to the mailbox without ever blocking the reader.
// A closed channel publishes nothing: its remote is gone from the config.
func (c *RemoteChannel) publish(ch RemoteChange) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.events.put(ch)
}

// changeFromReply parses the listings a "changed" event carries; a payload
// that does not parse is dropped so the receiver fetches instead.
func (c *RemoteChannel) changeFromReply(r remoteChannelReply) RemoteChange {
	ch := RemoteChange{Remote: c.name}
	if strings.TrimSpace(r.Sessions) == "" {
		return ch
	}
	sessions, err := parseRemoteSessions([]byte(r.Sessions))
	if err != nil {
		return ch
	}
	for i := range sessions {
		sessions[i].RemoteName = c.name
	}
	ch.Sessions = sessions
	ch.HasData = true
	trimmed := strings.TrimSpace(r.Groups)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var parsed groupListJSON
		if json.Unmarshal([]byte(trimmed), &parsed) == nil {
			ch.Groups = parseGroupListPaths(parsed)
		}
	}
	return ch
}

// markDown closes the current transport and fails every request in flight
// (never written: errChannelDown, the caller execs; written: the caller
// gets errChannelInterrupted); the next ensureConnected redials.
func (c *RemoteChannel) markDown() {
	c.mu.Lock()
	gen := c.gen
	c.mu.Unlock()
	c.markDownGen(gen)
}

// markDownGen is markDown for one specific transport: a no-op when a newer
// one has been dialled since.
func (c *RemoteChannel) markDownGen(gen uint64) {
	c.mu.Lock()
	if c.gen != gen {
		c.mu.Unlock()
		return
	}
	c.up.Store(false)
	if c.closeFn != nil {
		c.closeFn()
		c.closeFn = nil
	}
	c.stdin = nil
	c.watching = ""
	c.timeouts = 0
	pending := c.pending
	c.pending = map[int64]chan remoteChannelReply{}
	c.mu.Unlock()
	for _, ch := range pending {
		ch <- remoteChannelReply{Code: -1, Error: errChannelInterrupted.Error()}
	}
}

// Request runs args on the remote over the channel. It returns the command's
// stdout, or an error that names the exit status and stderr (the same shape
// SSHRunner.run produces), errChannelDown when the transport failed before
// the request went out, or errChannelInterrupted when it failed afterwards.
func (c *RemoteChannel) Request(ctx context.Context, args []string) ([]byte, error) {
	r, err := c.roundTrip(ctx, remoteChannelRequest{Args: args})
	if err != nil {
		return nil, err
	}
	return []byte(r.Stdout), nil
}

// Watching returns the session whose pane the remote is pushing over this
// channel, or "" when none is (also after a reconnect: the agent's watch
// did not survive, so the caller asks again).
func (c *RemoteChannel) Watching() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.watching
}

// PaneWatchSupported is false once the remote agent refused a watch request
// (older build); callers then keep polling the preview.
func (c *RemoteChannel) PaneWatchSupported() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.watchUnsupported
}

// Watch asks the remote agent to push pane events for sessionID (replacing
// any previous watch on this channel; the agent follows one pane at a
// time), trimmed to the last lines lines (0 for the whole capture). A watch
// already in place for the same session is a no-op. The session is recorded
// as watched before the request goes out so concurrent callers do not send
// it twice; a refusal by the agent clears it and marks pane watching
// unsupported on this channel.
func (c *RemoteChannel) Watch(ctx context.Context, sessionID string, lines int) error {
	if sessionID == "" {
		return c.Unwatch(ctx)
	}
	c.mu.Lock()
	if c.watchUnsupported {
		c.mu.Unlock()
		return errors.New("pane watch not supported by this remote")
	}
	if c.watching == sessionID {
		c.mu.Unlock()
		return nil
	}
	c.watching = sessionID
	c.mu.Unlock()
	_, err := c.roundTrip(ctx, remoteChannelRequest{Watch: sessionID, Lines: lines})
	if err != nil {
		c.mu.Lock()
		if c.watching == sessionID {
			c.watching = ""
		}
		if !isChannelTransportErr(err) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			c.watchUnsupported = true
		}
		c.mu.Unlock()
	}
	return err
}

// Unwatch stops the pane watch on this channel, if any.
func (c *RemoteChannel) Unwatch(ctx context.Context) error {
	c.mu.Lock()
	if c.watching == "" {
		c.mu.Unlock()
		return nil
	}
	c.watching = ""
	c.mu.Unlock()
	_, err := c.roundTrip(ctx, remoteChannelRequest{Unwatch: true})
	return err
}

// roundTrip sends one request and waits for its reply, mapping a failed
// command to the same error shape SSHRunner.run produces, a transport that
// failed before the write to errChannelDown and one that failed after it to
// errChannelInterrupted. Consecutive deadline misses take the transport
// down (#5): a half-open ssh link accepts writes forever and answers none.
func (c *RemoteChannel) roundTrip(ctx context.Context, req remoteChannelRequest) (remoteChannelReply, error) {
	if !c.up.Load() {
		// The channel outlives this request, so its dial is not bound to
		// the request's context.
		go c.ensureConnected() //nolint:gosec // see comment above
		return remoteChannelReply{}, errChannelDown
	}
	id := c.nextID.Add(1)
	reply := make(chan remoteChannelReply, 1)
	c.mu.Lock()
	stdin, gen := c.stdin, c.gen
	if stdin == nil {
		c.mu.Unlock()
		return remoteChannelReply{}, errChannelDown
	}
	c.pending[id] = reply
	c.mu.Unlock()

	req.ID = id
	line, _ := json.Marshal(req)
	if n, err := stdin.Write(append(line, '\n')); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		c.markDownGen(gen)
		if n > 0 {
			// Part of the line may have reached the agent; it cannot have
			// been executed without the newline, but be conservative.
			return remoteChannelReply{}, errChannelInterrupted
		}
		return remoteChannelReply{}, errChannelDown
	}
	select {
	case r := <-reply:
		if r.Code == -1 && r.Error == errChannelInterrupted.Error() {
			// markDownGen's verdict for a request that was on the wire.
			return r, errChannelInterrupted
		}
		if r.Error != "" {
			return r, fmt.Errorf("ssh command failed: %s", r.Error)
		}
		if r.Code != 0 {
			detail := r.Stderr
			if strings.TrimSpace(detail) == "" {
				detail = strings.TrimSpace(r.Stdout)
			}
			return r, fmt.Errorf("ssh command failed: exit status %d: %s", r.Code, detail)
		}
		return r, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		drop := false
		if errors.Is(ctx.Err(), context.DeadlineExceeded) && c.gen == gen {
			c.timeouts++
			drop = c.timeouts >= remoteChannelTimeoutsToDrop
		}
		c.mu.Unlock()
		if drop {
			sessionLog.Debug("remote_channel_timeouts", slog.String("remote", c.name))
			c.markDownGen(gen)
		}
		return remoteChannelReply{}, ctx.Err()
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
