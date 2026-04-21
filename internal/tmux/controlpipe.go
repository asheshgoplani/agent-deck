package tmux

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

var pipeLog = logging.ForComponent("pipe")

const (
	controlPipeConnectAttempts  = 3
	controlPipeHandshakeTimeout = 4 * time.Second
	controlPipeCommandTimeout   = 5 * time.Second
	controlPipeRetryBackoff     = 150 * time.Millisecond
)

// ControlPipe wraps a persistent `tmux -C attach-session -t <name>` process.
// It provides event-driven output detection via %output events and
// zero-subprocess command execution through the stdin/stdout pipe.
type ControlPipe struct {
	sessionName string
	socketName  string // tmux -L value; "" means user's default server
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	stdout      io.ReadCloser

	// Event channel: fires when the session produces output
	outputEvents chan struct{}

	// Event channel: fires when a window is added or closed
	windowEvents chan struct{}

	// Command/response serialization
	cmdMu      sync.Mutex
	responseCh chan commandResponse

	// Readiness: signaled after initial %begin/%end handshake consumed
	ready        chan struct{}
	readyOnce    sync.Once
	handshakeErr error // non-nil if handshake received %error (e.g. session not found)

	// State
	mu         sync.RWMutex
	alive      bool
	lastOutput time.Time

	// Lifecycle
	done      chan struct{}
	closeOnce sync.Once

	// waitOnce guards cmd.Wait() so exactly one of reader() (natural EOF)
	// and Close() (manual shutdown) reaps the child. Without this, a tmux
	// subprocess that exited on its own became a zombie until Close() was
	// eventually called — or forever if it never was (#677).
	waitOnce sync.Once
}

type commandResponse struct {
	output string
	err    error
}

// NewControlPipe starts a tmux control mode pipe attached to the given session
// on the given socket. socketName is the tmux `-L <name>` selector captured at
// session-creation time (Instance.TmuxSocketName / Session.SocketName); pass ""
// to target the user's default tmux server. Blocks until the initial handshake
// completes (or a short timeout), so the pipe is ready for SendCommand
// immediately after return. Retries a few times to smooth over transient
// tmux/control-mode startup failures.
func NewControlPipe(sessionName, socketName string) (*ControlPipe, error) {
	var lastErr error
	for attempt := 1; attempt <= controlPipeConnectAttempts; attempt++ {
		cp, err := newControlPipeOnce(sessionName, socketName)
		if err == nil {
			return cp, nil
		}
		lastErr = err
		if attempt == controlPipeConnectAttempts {
			break
		}
		pipeLog.Debug(
			"pipe_connect_retry",
			slog.String("session", sessionName),
			slog.String("socket", socketName),
			slog.Int("attempt", attempt),
			slog.String("error", err.Error()),
		)
		time.Sleep(time.Duration(attempt) * controlPipeRetryBackoff)
	}
	return nil, lastErr
}

func newControlPipeOnce(sessionName, socketName string) (*ControlPipe, error) {
	cmd := tmuxExec(socketName, "-C", "attach-session", "-t", sessionName)
	// Put in own process group so we can kill the entire group on shutdown
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("start tmux -C: %w", err)
	}

	cp := &ControlPipe{
		sessionName:  sessionName,
		socketName:   socketName,
		cmd:          cmd,
		stdin:        stdin,
		stdout:       stdout,
		outputEvents: make(chan struct{}, 64),
		windowEvents: make(chan struct{}, 8),
		responseCh:   make(chan commandResponse, 1),
		ready:        make(chan struct{}),
		alive:        true,
		done:         make(chan struct{}),
	}

	go cp.reader()

	// Wait for initial handshake to complete so the pipe is ready for commands.
	// tmux sends a %begin/%end pair on connect; we must consume it before
	// any SendCommand call, otherwise the response gets mixed up.
	select {
	case <-cp.ready:
	case <-cp.done:
		cp.Close()
		return nil, fmt.Errorf("pipe died during handshake for session %s", sessionName)
	case <-time.After(controlPipeHandshakeTimeout):
		// Timeout waiting for handshake, but pipe may still work
		pipeLog.Debug("pipe_handshake_timeout", slog.String("session", sessionName))
	}

	// Check if handshake received an error (e.g. "can't find session")
	if cp.handshakeErr != nil {
		cp.Close()
		return nil, fmt.Errorf("session %s: %w", sessionName, cp.handshakeErr)
	}

	pipeLog.Debug("pipe_connected", slog.String("session", sessionName))
	return cp, nil
}

// reap calls cmd.Wait() exactly once, harvesting the child's exit status and
// freeing its process-table entry. Safe to call from any goroutine; extra
// calls are no-ops. Required to prevent zombie accumulation (#677).
func (cp *ControlPipe) reap() {
	cp.waitOnce.Do(func() {
		_ = cp.cmd.Wait()
	})
}

// reader is the goroutine that parses tmux control mode protocol events.
// It handles %output, %begin/%end/%error for command responses, and
// silently skips all other %-prefixed control lines.
func (cp *ControlPipe) reader() {
	defer func() {
		cp.mu.Lock()
		cp.alive = false
		cp.mu.Unlock()
		// Reap the child before signaling done so anyone watching Done()
		// can rely on the process being fully torn down (#677).
		cp.reap()
		close(cp.done)
		pipeLog.Debug("pipe_reader_exited", slog.String("session", cp.sessionName))
	}()

	scanner := bufio.NewScanner(cp.stdout)
	// 2MB buffer for large capture-pane outputs
	scanner.Buffer(make([]byte, 2*1024*1024), 2*1024*1024)

	var (
		inCapture bool
		lines     []string
		isReady   bool // tracks whether initial handshake has completed
	)

	for scanner.Scan() {
		raw := scanner.Text()

		// All %-prefixed lines are control mode protocol messages
		if strings.HasPrefix(raw, "%") {
			if strings.HasPrefix(raw, "%output") {
				cp.mu.Lock()
				cp.lastOutput = time.Now()
				cp.mu.Unlock()

				// Non-blocking send to output events channel
				select {
				case cp.outputEvents <- struct{}{}:
				default:
				}
			} else if strings.HasPrefix(raw, "%window-add") || strings.HasPrefix(raw, "%window-close") {
				// Window created or closed — notify listeners
				select {
				case cp.windowEvents <- struct{}{}:
				default:
				}
			} else if strings.HasPrefix(raw, "%begin ") {
				inCapture = true
				lines = lines[:0]
			} else if strings.HasPrefix(raw, "%end ") {
				inCapture = false
				if !isReady {
					// First %end completes the initial handshake.
					// Discard this response (it's the attach acknowledgment).
					isReady = true
					cp.readyOnce.Do(func() { close(cp.ready) })
					continue
				}
				result := strings.Join(lines, "\n")
				select {
				case cp.responseCh <- commandResponse{output: result}:
				default:
					pipeLog.Debug("response_dropped", slog.String("session", cp.sessionName))
				}
			} else if strings.HasPrefix(raw, "%error ") {
				inCapture = false
				if !isReady {
					// Handshake got an error (typically "can't find session").
					// Record the error so NewControlPipe can detect non-existent sessions.
					parts := strings.Fields(raw)
					if len(parts) > 3 {
						cp.handshakeErr = fmt.Errorf("%s", strings.Join(parts[3:], " "))
					} else {
						cp.handshakeErr = fmt.Errorf("handshake error: %s", raw)
					}
					isReady = true
					cp.readyOnce.Do(func() { close(cp.ready) })
					continue
				}
				errMsg := raw
				parts := strings.Fields(raw)
				if len(parts) > 3 {
					errMsg = strings.Join(parts[3:], " ")
				}
				select {
				case cp.responseCh <- commandResponse{err: fmt.Errorf("tmux error: %s", errMsg)}:
				default:
				}
			}
			// All other % lines (%exit, %session-changed, etc.) silently skipped.
			// Critical: must NOT fall through to inCapture collection below,
			// because %output events interleave with capture-pane response data.
			continue
		}

		// Non-% lines during capture collection are response data
		if inCapture {
			lines = append(lines, raw)
		}
	}

	if err := scanner.Err(); err != nil {
		pipeLog.Debug("pipe_scanner_error", slog.String("session", cp.sessionName), slog.String("error", err.Error()))
	}
}

// SendCommand sends a command through the control mode pipe and waits for the response.
// Commands are serialized via cmdMu. Returns the response text or an error.
// Timeout is slightly relaxed to reduce false negatives when tmux is busy.
func (cp *ControlPipe) SendCommand(command string) (string, error) {
	cp.mu.RLock()
	if !cp.alive {
		cp.mu.RUnlock()
		return "", fmt.Errorf("pipe not alive for session %s", cp.sessionName)
	}
	cp.mu.RUnlock()

	cp.cmdMu.Lock()
	defer cp.cmdMu.Unlock()

	// Drain any stale response
	select {
	case <-cp.responseCh:
	default:
	}

	// Send command through stdin
	_, err := fmt.Fprintln(cp.stdin, command)
	if err != nil {
		return "", fmt.Errorf("write to pipe: %w", err)
	}

	// Wait for response with timeout
	select {
	case resp := <-cp.responseCh:
		if resp.err != nil {
			return "", resp.err
		}
		return resp.output, nil
	case <-time.After(controlPipeCommandTimeout):
		return "", fmt.Errorf("command timed out after %s: %s", controlPipeCommandTimeout, command)
	case <-cp.done:
		return "", fmt.Errorf("pipe closed during command: %s", command)
	}
}

// CapturePaneVia sends capture-pane through the control mode pipe.
// Returns the pane content without spawning any subprocess.
func (cp *ControlPipe) CapturePaneVia() (string, error) {
	return cp.SendCommand(fmt.Sprintf("capture-pane -t %s -p -e", cp.sessionName))
}

// OutputEvents returns a channel that fires when the session produces output.
// Multiple rapid outputs may be coalesced into fewer channel sends.
func (cp *ControlPipe) OutputEvents() <-chan struct{} {
	return cp.outputEvents
}

// WindowEvents returns a channel that fires when a window is added or closed.
func (cp *ControlPipe) WindowEvents() <-chan struct{} {
	return cp.windowEvents
}

// LastOutputTime returns the time of the most recent %output event.
func (cp *ControlPipe) LastOutputTime() time.Time {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.lastOutput
}

// IsAlive returns true if the control mode process is still running.
func (cp *ControlPipe) IsAlive() bool {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.alive
}

// Done returns a channel that closes when the pipe exits.
func (cp *ControlPipe) Done() <-chan struct{} {
	return cp.done
}

// Close shuts down the control mode pipe and kills the process.
func (cp *ControlPipe) Close() {
	cp.closeOnce.Do(func() {
		cp.mu.Lock()
		cp.alive = false
		cp.mu.Unlock()

		// Close stdin first (tells tmux to disconnect)
		cp.stdin.Close()

		// Kill the process group to clean up reliably
		if cp.cmd.Process != nil {
			pgid, err := syscall.Getpgid(cp.cmd.Process.Pid)
			if err == nil {
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			} else {
				_ = cp.cmd.Process.Kill()
			}
		}

		// Wait for the process to exit (prevents zombies). Routed through
		// reap() so reader() and Close() cannot double-Wait (#677).
		cp.reap()

		pipeLog.Debug("pipe_closed", slog.String("session", cp.sessionName))
	})
}
