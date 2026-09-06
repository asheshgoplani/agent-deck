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
	name    string
	dial    func(ctx context.Context) (io.WriteCloser, io.Reader, func(), error)
	events  chan<- RemoteChange
	mu      sync.Mutex
	stdin   io.WriteCloser
	closeFn func()
	pending map[int64]chan remoteChannelReply
	nextID  atomic.Int64
	up      atomic.Bool
	// unsupportedUntil is set when the remote does not know remote-agent
	// (old build): no reconnect attempts until then.
	unsupportedUntil time.Time
	lastAttempt      time.Time
	backoff          time.Duration
	dialing          bool
	// watching is the session whose pane the remote agent is pushing for
	// this channel ("" for none); reset when the transport drops, because
	// the agent's watch dies with it. watchUnsupported is set when the
	// agent answered a watch request with an error (a build that predates
	// pane watching): the caller then polls the preview as before.
	watching         string
	watchUnsupported bool
}

type remoteChannelRequest struct {
	ID      int64    `json:"id"`
	Args    []string `json:"args,omitempty"`
	Watch   string   `json:"watch,omitempty"`
	Unwatch bool     `json:"unwatch,omitempty"`
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

// errChannelDown means the transport failed, not the remote command; callers
// fall back to a plain ssh exec.
var errChannelDown = errors.New("remote channel down")

// remoteChangeEvents fans in "changed" pushes from every remote for the TUI.
// Buffered and non-blocking on the sending side: a burst collapses into a few
// pending refreshes, never a stall on the reader goroutine.
var remoteChangeEvents = make(chan RemoteChange, 32)

// RemoteChangeEvents delivers pushed changes, one per remote change.
func RemoteChangeEvents() <-chan RemoteChange { return remoteChangeEvents }

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

// channelFor returns the shared channel for a runner's remote, starting it
// on first use. nil when channels are disabled or the runner is unnamed.
func channelFor(r *SSHRunner) *RemoteChannel {
	if r == nil || r.name == "" || r.runFn != nil || !remoteChannelsEnabled() {
		return nil
	}
	remoteChannelsMu.Lock()
	defer remoteChannelsMu.Unlock()
	if ch, ok := remoteChannels[r.name]; ok {
		return ch
	}
	rc := *r
	ch := &RemoteChannel{
		name:    r.name,
		events:  remoteChangeEvents,
		pending: map[int64]chan remoteChannelReply{},
		backoff: 2 * time.Second,
		dial: func(ctx context.Context) (io.WriteCloser, io.Reader, func(), error) {
			return rc.dialRemoteAgent(ctx)
		},
	}
	remoteChannels[r.name] = ch
	go ch.ensureConnected()
	return ch
}

// dialRemoteAgent starts `ssh host agent-deck -p profile remote-agent` with
// pipes on both ends, over the same ControlMaster socket every other command
// uses.
func (r *SSHRunner) dialRemoteAgent(ctx context.Context) (io.WriteCloser, io.Reader, func(), error) {
	if err := ValidateSSHHost(r.Host); err != nil {
		return nil, nil, nil, err
	}
	_ = os.MkdirAll(sshControlDir, 0700)
	// Same argv construction as every other ssh exec in this file (host
	// validated above, options fixed, remote command shell-quoted).
	cmd := exec.CommandContext(ctx, "ssh", r.sshBaseArgs(r.buildRemoteCommand("remote-agent"))...) //nolint:gosec // see comment above
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

// ensureConnected dials if the channel is down and a retry is due. It never
// blocks a caller: connecting happens on its own goroutine and requests made
// meanwhile fall back to ssh exec.
func (c *RemoteChannel) ensureConnected() {
	c.mu.Lock()
	if c.up.Load() || c.dialing || time.Now().Before(c.unsupportedUntil) || time.Since(c.lastAttempt) < c.backoff {
		c.mu.Unlock()
		return
	}
	c.dialing = true
	c.lastAttempt = time.Now()
	c.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	stdin, stdout, closeFn, err := c.dial(ctx)
	if err != nil {
		cancel()
		c.mu.Lock()
		c.dialing = false
		c.backoff = minDuration(c.backoff*2, 30*time.Second)
		c.mu.Unlock()
		return
	}
	// The agent announces itself; anything else (an old build printing
	// "Unknown command") means no channel on this remote for a while.
	reader := bufio.NewReader(stdout)
	firstLine, rerr := readLineWithin(reader, 15*time.Second)
	var hello remoteChannelReply
	if rerr != nil || json.Unmarshal([]byte(firstLine), &hello) != nil || hello.Event != "ready" {
		closeFn()
		cancel()
		c.mu.Lock()
		c.dialing = false
		c.unsupportedUntil = time.Now().Add(10 * time.Minute)
		c.mu.Unlock()
		return
	}
	c.mu.Lock()
	c.stdin = stdin
	c.closeFn = func() { closeFn(); cancel() }
	c.dialing = false
	c.backoff = 2 * time.Second
	c.up.Store(true)
	c.mu.Unlock()
	go c.readLoop(reader)
}

func readLineWithin(r *bufio.Reader, d time.Duration) (string, error) {
	type res struct {
		s   string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		s, err := r.ReadString('\n')
		ch <- res{s, err}
	}()
	select {
	case v := <-ch:
		return strings.TrimSpace(v.s), v.err
	case <-time.After(d):
		return "", errors.New("timeout waiting for remote-agent")
	}
}

func (c *RemoteChannel) readLoop(r *bufio.Reader) {
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			c.markDown()
			return
		}
		var reply remoteChannelReply
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &reply) != nil {
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

// publish hands an event to the fan-in without ever blocking the reader: a
// full buffer drops the event (a "changed" push is followed by a poll, a
// "pane" push by the next change of that pane).
func (c *RemoteChannel) publish(ch RemoteChange) {
	select {
	case c.events <- ch:
	default:
	}
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

// markDown closes the transport and fails every request in flight so the
// caller falls back to exec; the next ensureConnected redials.
func (c *RemoteChannel) markDown() {
	c.mu.Lock()
	c.up.Store(false)
	if c.closeFn != nil {
		c.closeFn()
		c.closeFn = nil
	}
	c.stdin = nil
	c.watching = ""
	pending := c.pending
	c.pending = map[int64]chan remoteChannelReply{}
	c.mu.Unlock()
	for _, ch := range pending {
		ch <- remoteChannelReply{Code: -1, Error: errChannelDown.Error()}
	}
}

// Request runs args on the remote over the channel. It returns the command's
// stdout, or an error that names the exit status and stderr (the same shape
// SSHRunner.run produces), or errChannelDown when the transport failed.
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
// time). A watch already in place for the same session is a no-op. The
// session is recorded as watched before the request goes out so concurrent
// callers do not send it twice; a refusal by the agent clears it and marks
// pane watching unsupported on this channel.
func (c *RemoteChannel) Watch(ctx context.Context, sessionID string) error {
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
	_, err := c.roundTrip(ctx, remoteChannelRequest{Watch: sessionID})
	if err != nil {
		c.mu.Lock()
		if c.watching == sessionID {
			c.watching = ""
		}
		if !errors.Is(err, errChannelDown) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
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
// command to the same error shape SSHRunner.run produces and a dead
// transport to errChannelDown.
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
	stdin := c.stdin
	if stdin == nil {
		c.mu.Unlock()
		return remoteChannelReply{}, errChannelDown
	}
	c.pending[id] = reply
	c.mu.Unlock()

	req.ID = id
	line, _ := json.Marshal(req)
	if _, err := stdin.Write(append(line, '\n')); err != nil {
		c.markDown()
		return remoteChannelReply{}, errChannelDown
	}
	select {
	case r := <-reply:
		if r.Code == -1 && r.Error == errChannelDown.Error() {
			return r, errChannelDown
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
		c.mu.Unlock()
		return remoteChannelReply{}, ctx.Err()
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
