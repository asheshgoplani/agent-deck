//go:build !windows
// +build !windows

package tmux

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/ssh"
	"github.com/creack/pty"
	"golang.org/x/term"
)

// SSHExecutor executes tmux commands on a remote host via SSH
type SSHExecutor struct {
	conn    *ssh.Connection
	hostID  string
	tmuxCmd string // Path to tmux binary (default: "tmux")
}

// NewSSHExecutor creates a new SSH executor for the given connection
func NewSSHExecutor(conn *ssh.Connection, hostID string) *SSHExecutor {
	return &SSHExecutor{
		conn:    conn,
		hostID:  hostID,
		tmuxCmd: "tmux",
	}
}

// NewSSHExecutorWithTmuxPath creates a new SSH executor with a custom tmux path
func NewSSHExecutorWithTmuxPath(conn *ssh.Connection, hostID, tmuxPath string) *SSHExecutor {
	cmd := "tmux"
	if tmuxPath != "" {
		cmd = tmuxPath
	}
	return &SSHExecutor{
		conn:    conn,
		hostID:  hostID,
		tmuxCmd: cmd,
	}
}

// NewSSHExecutorFromPool creates an SSH executor using the connection pool
func NewSSHExecutorFromPool(hostID string) (*SSHExecutor, error) {
	pool := ssh.DefaultPool()
	conn, err := pool.Get(hostID)
	if err != nil {
		return nil, fmt.Errorf("failed to get SSH connection for %s: %w", hostID, err)
	}

	// Look up tmux path from config
	tmuxPath := "tmux"
	if cfg, exists := pool.GetConfig(hostID); exists && cfg.TmuxPath != "" {
		tmuxPath = cfg.TmuxPath
	}

	return NewSSHExecutorWithTmuxPath(conn, hostID, tmuxPath), nil
}

// runRemote executes a command on the remote host and returns the output
func (e *SSHExecutor) runRemote(command string) (string, error) {
	return e.conn.RunCommand(command)
}

// RunCommand executes an arbitrary command on the remote host
// This is a public wrapper for runRemote for use by external packages
func (e *SSHExecutor) RunCommand(command string) (string, error) {
	return e.runRemote(command)
}

// runRemoteIgnoreError executes a command and ignores errors (for optional operations)
func (e *SSHExecutor) runRemoteIgnoreError(command string) {
	_, _ = e.conn.RunCommand(command)
}

// NewSession creates a new tmux session on the remote host
func (e *SSHExecutor) NewSession(name, workDir string) error {
	if workDir == "" {
		workDir = "~"
	}
	cmd := fmt.Sprintf("%s new-session -d -s %q -c %q", e.tmuxCmd, name, workDir)
	_, err := e.runRemote(cmd)
	if err != nil {
		return fmt.Errorf("failed to create remote tmux session: %w", err)
	}
	return nil
}

// KillSession terminates a tmux session on the remote host
func (e *SSHExecutor) KillSession(name string) error {
	cmd := fmt.Sprintf("%s kill-session -t %q", e.tmuxCmd, name)
	_, err := e.runRemote(cmd)
	return err
}

// SessionExists checks if a session exists on the remote host
func (e *SSHExecutor) SessionExists(name string) (bool, error) {
	cmd := fmt.Sprintf("%s has-session -t %q 2>/dev/null && echo exists || echo notfound", e.tmuxCmd, name)
	output, err := e.runRemote(cmd)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(output) == "exists", nil
}

// ListSessions returns all sessions with their window activity timestamps
func (e *SSHExecutor) ListSessions() (map[string]int64, error) {
	cmd := fmt.Sprintf("%s list-windows -a -F '#{session_name}\t#{window_activity}' 2>/dev/null || echo ''", e.tmuxCmd)
	output, err := e.runRemote(cmd)
	if err != nil {
		return nil, err
	}

	sessions := make(map[string]int64)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		name := parts[0]
		var activity int64
		_, _ = fmt.Sscanf(parts[1], "%d", &activity)
		if existing, ok := sessions[name]; !ok || activity > existing {
			sessions[name] = activity
		}
	}
	return sessions, nil
}

// SendKeys sends keys to a session on the remote host
func (e *SSHExecutor) SendKeys(session, keys string, literal bool) error {
	var cmd string
	if literal {
		// Escape the keys for shell and tmux
		escaped := strings.ReplaceAll(keys, "'", "'\\''")
		cmd = fmt.Sprintf("%s send-keys -l -t %q '%s'", e.tmuxCmd, session, escaped)
	} else {
		cmd = fmt.Sprintf("%s send-keys -t %q %s", e.tmuxCmd, session, keys)
	}
	_, err := e.runRemote(cmd)
	return err
}

// CapturePane captures the visible content of a pane on the remote host
func (e *SSHExecutor) CapturePane(session string, joinWrapped bool) (string, error) {
	joinFlag := ""
	if joinWrapped {
		joinFlag = " -J"
	}
	cmd := fmt.Sprintf("%s capture-pane -t %q -p%s", e.tmuxCmd, session, joinFlag)
	output, err := e.runRemote(cmd)
	if err != nil {
		return "", fmt.Errorf("failed to capture remote pane: %w", err)
	}
	return output, nil
}

// CapturePaneHistory captures scrollback history from the remote host
func (e *SSHExecutor) CapturePaneHistory(session string, lines int) (string, error) {
	cmd := fmt.Sprintf("%s capture-pane -t %q -p -J -S -%d", e.tmuxCmd, session, lines)
	output, err := e.runRemote(cmd)
	if err != nil {
		return "", fmt.Errorf("failed to capture remote history: %w", err)
	}
	return output, nil
}

// RespawnPane kills the current process and starts a new command on the remote host
func (e *SSHExecutor) RespawnPane(session, command string) error {
	target := session + ":"
	var cmd string
	if command != "" {
		// Escape command for remote shell
		escaped := strings.ReplaceAll(command, "'", "'\\''")
		cmd = fmt.Sprintf("%s respawn-pane -k -t %q 'bash -ic %q'", e.tmuxCmd, target, escaped)
	} else {
		cmd = fmt.Sprintf("%s respawn-pane -k -t %q", e.tmuxCmd, target)
	}
	_, err := e.runRemote(cmd)
	if err != nil {
		return fmt.Errorf("failed to respawn remote pane: %w", err)
	}
	return nil
}

// SetOption sets a tmux option for a session on the remote host
func (e *SSHExecutor) SetOption(session, option, value string) error {
	cmd := fmt.Sprintf("%s set-option -t %q %s %q 2>/dev/null || true", e.tmuxCmd, session, option, value)
	e.runRemoteIgnoreError(cmd)
	return nil
}

// SetServerOption sets a server-wide tmux option on the remote host
func (e *SSHExecutor) SetServerOption(option, value string) error {
	cmd := fmt.Sprintf("%s set -asq %s %q 2>/dev/null || true", e.tmuxCmd, option, value)
	e.runRemoteIgnoreError(cmd)
	return nil
}

// SetEnvironment sets an environment variable for a session on the remote host
func (e *SSHExecutor) SetEnvironment(session, key, value string) error {
	cmd := fmt.Sprintf("%s set-environment -t %q %s %q", e.tmuxCmd, session, key, value)
	_, err := e.runRemote(cmd)
	return err
}

// GetEnvironment gets an environment variable from a session on the remote host
func (e *SSHExecutor) GetEnvironment(session, key string) (string, error) {
	cmd := fmt.Sprintf("%s show-environment -t %q %s 2>/dev/null", e.tmuxCmd, session, key)
	output, err := e.runRemote(cmd)
	if err != nil {
		return "", fmt.Errorf("variable not found: %s", key)
	}
	line := strings.TrimSpace(output)
	prefix := key + "="
	if strings.HasPrefix(line, prefix) {
		return strings.TrimPrefix(line, prefix), nil
	}
	return "", fmt.Errorf("variable not found: %s", key)
}

// DisplayMessage runs tmux display-message on the remote host
func (e *SSHExecutor) DisplayMessage(session, format string) (string, error) {
	cmd := fmt.Sprintf("%s display-message -t %q -p %q", e.tmuxCmd, session, format)
	output, err := e.runRemote(cmd)
	if err != nil {
		return "", fmt.Errorf("failed to display message: %w", err)
	}
	return strings.TrimSpace(output), nil
}

// EnablePipePane enables pipe-pane on the remote host
// Note: This logs to a file on the REMOTE host, not locally
func (e *SSHExecutor) EnablePipePane(session, outputFile string) error {
	// Create log directory on remote
	logDir := "~/.agent-deck/logs"
	e.runRemoteIgnoreError(fmt.Sprintf("mkdir -p %s", logDir))

	cmd := fmt.Sprintf("%s pipe-pane -t %q -o \"cat >> '%s'\"", e.tmuxCmd, session, outputFile)
	_, err := e.runRemote(cmd)
	return err
}

// DisablePipePane disables pipe-pane on the remote host
func (e *SSHExecutor) DisablePipePane(session string) error {
	cmd := fmt.Sprintf("%s pipe-pane -t %q", e.tmuxCmd, session)
	_, err := e.runRemote(cmd)
	return err
}

// Attach attaches to a tmux session on the remote host with PTY support
func (e *SSHExecutor) Attach(ctx context.Context, session string, stdin io.Reader, stdout, stderr io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Build SSH command for interactive attach
	// ssh -t user@host tmux attach-session -t session
	sshCmd, err := e.conn.StartInteractiveSession(fmt.Sprintf("%s attach-session -t %q", e.tmuxCmd, session))
	if err != nil {
		return fmt.Errorf("failed to start SSH session: %w", err)
	}

	// Start command with PTY
	ptmx, err := pty.Start(sshCmd)
	if err != nil {
		return fmt.Errorf("failed to start pty: %w", err)
	}
	defer ptmx.Close()

	// Get stdin as file for raw mode
	stdinFile, ok := stdin.(*os.File)
	if !ok {
		stdinFile = os.Stdin
	}

	// Save original terminal state and set raw mode
	oldState, err := term.MakeRaw(int(stdinFile.Fd()))
	if err != nil {
		return fmt.Errorf("failed to set raw mode: %w", err)
	}
	defer func() { _ = term.Restore(int(stdinFile.Fd()), oldState) }()

	// Handle window resize signals
	sigwinch := make(chan os.Signal, 1)
	signal.Notify(sigwinch, syscall.SIGWINCH)
	sigwinchDone := make(chan struct{})
	defer func() {
		signal.Stop(sigwinch)
		close(sigwinchDone)
	}()

	var wg sync.WaitGroup

	// SIGWINCH handler goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-sigwinchDone:
				return
			case _, ok := <-sigwinch:
				if !ok {
					return
				}
				if ws, err := pty.GetsizeFull(stdinFile); err == nil {
					_ = pty.Setsize(ptmx, ws)
				}
			}
		}
	}()
	// Initial resize
	sigwinch <- syscall.SIGWINCH

	// Channel to signal detach via Ctrl+Q
	detachCh := make(chan struct{})
	ioErrors := make(chan error, 2)

	// Timeout to ignore initial terminal control sequences
	startTime := time.Now()
	const controlSeqTimeout = 50 * time.Millisecond

	// Goroutine: Copy PTY output to stdout
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := io.Copy(stdout, ptmx)
		if err != nil && err != io.EOF {
			select {
			case ioErrors <- fmt.Errorf("PTY read error: %w", err):
			default:
			}
		}
	}()

	// Goroutine: Read stdin, intercept Ctrl+Q, forward rest to PTY
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 32)
		for {
			n, err := stdin.Read(buf)
			if err != nil {
				if err == io.EOF {
					break
				}
				select {
				case ioErrors <- fmt.Errorf("stdin read error: %w", err):
				default:
				}
				return
			}

			// Discard initial terminal control sequences
			if time.Since(startTime) < controlSeqTimeout {
				continue
			}

			// Check for Ctrl+Q (ASCII 17)
			if n == 1 && buf[0] == 17 {
				close(detachCh)
				cancel()
				return
			}

			// Forward to PTY
			if _, err := ptmx.Write(buf[:n]); err != nil {
				select {
				case ioErrors <- fmt.Errorf("PTY write error: %w", err):
				default:
				}
				return
			}
		}
	}()

	// Wait for command to finish
	cmdDone := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		cmdDone <- sshCmd.Wait()
	}()

	// Wait for detach or completion
	select {
	case <-detachCh:
		return nil
	case err := <-cmdDone:
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				if exitErr.ExitCode() == 0 || exitErr.ExitCode() == 1 {
					return nil
				}
			}
			if ctx.Err() != nil {
				return nil
			}
		}
		return err
	case <-ctx.Done():
		return nil
	}
}

// IsRemote returns true for SSH executor
func (e *SSHExecutor) IsRemote() bool {
	return true
}

// HostID returns the host identifier for this executor
func (e *SSHExecutor) HostID() string {
	return e.hostID
}

// SessionInfo contains extended information about a tmux session
type SessionInfo struct {
	WorkingDir string // Working directory of the session's first window
	Activity   int64  // Last activity timestamp
}

// ListSessionsWithInfo returns all sessions with their working directories and activity timestamps
// This is used for remote session discovery to get additional context about each session
func (e *SSHExecutor) ListSessionsWithInfo() (map[string]SessionInfo, error) {
	// Query session name, working directory, and activity for each session
	// We use the first window (index 0) for working directory as sessions are typically single-window
	cmd := fmt.Sprintf("%s list-sessions -F '#{session_name}\t#{session_path}\t#{session_activity}' 2>/dev/null || echo ''", e.tmuxCmd)
	output, err := e.runRemote(cmd)
	if err != nil {
		return nil, err
	}

	sessions := make(map[string]SessionInfo)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 1 {
			continue
		}

		name := parts[0]
		var workDir string
		var activity int64

		if len(parts) >= 2 {
			workDir = parts[1]
		}
		if len(parts) >= 3 {
			_, _ = fmt.Sscanf(parts[2], "%d", &activity)
		}

		sessions[name] = SessionInfo{
			WorkingDir: workDir,
			Activity:   activity,
		}
	}

	return sessions, nil
}
