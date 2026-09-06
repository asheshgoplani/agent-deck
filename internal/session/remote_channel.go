package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	events  chan<- string
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
}

type remoteChannelRequest struct {
	ID   int64    `json:"id"`
	Args []string `json:"args"`
}

type remoteChannelReply struct {
	ID     int64  `json:"id,omitempty"`
	Event  string `json:"event,omitempty"`
	Stdout string `json:"stdout,omitempty"`
	Stderr string `json:"stderr,omitempty"`
	Code   int    `json:"code"`
	Error  string `json:"error,omitempty"`
}

// errChannelDown means the transport failed, not the remote command; callers
// fall back to a plain ssh exec.
var errChannelDown = errors.New("remote channel down")

// remoteChangeEvents fans in "changed" pushes from every remote for the TUI.
// Buffered and non-blocking on the sending side: a burst collapses into a few
// pending refreshes, never a stall on the reader goroutine.
var remoteChangeEvents = make(chan string, 32)

// RemoteChangeEvents delivers the name of a remote whose state changed.
func RemoteChangeEvents() <-chan string { return remoteChangeEvents }

var (
	remoteChannelsMu sync.Mutex
	remoteChannels   = map[string]*RemoteChannel{}
)

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
	cmd := exec.CommandContext(ctx, "ssh", r.sshBaseArgs(r.buildRemoteCommand("remote-agent"))...)
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
			select {
			case c.events <- c.name:
			default:
			}
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
	if !c.up.Load() {
		go c.ensureConnected()
		return nil, errChannelDown
	}
	id := c.nextID.Add(1)
	reply := make(chan remoteChannelReply, 1)
	c.mu.Lock()
	stdin := c.stdin
	if stdin == nil {
		c.mu.Unlock()
		return nil, errChannelDown
	}
	c.pending[id] = reply
	c.mu.Unlock()

	line, _ := json.Marshal(remoteChannelRequest{ID: id, Args: args})
	if _, err := stdin.Write(append(line, '\n')); err != nil {
		c.markDown()
		return nil, errChannelDown
	}
	select {
	case r := <-reply:
		if r.Code == -1 && r.Error == errChannelDown.Error() {
			return nil, errChannelDown
		}
		if r.Error != "" {
			return nil, fmt.Errorf("ssh command failed: %s", r.Error)
		}
		if r.Code != 0 {
			detail := r.Stderr
			if strings.TrimSpace(detail) == "" {
				detail = strings.TrimSpace(r.Stdout)
			}
			return nil, fmt.Errorf("ssh command failed: exit status %d: %s", r.Code, detail)
		}
		return []byte(r.Stdout), nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
