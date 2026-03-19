//go:build !windows
// +build !windows

package tmux

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/terminal"
	"github.com/creack/pty"
	"golang.org/x/term"
)

// IndexDetachKey returns the index of a control-key sequence in data, or -1 if
// not found. detachByte is the raw ASCII byte (e.g. 0x11 for Ctrl+Q).
// Handles three encodings:
//   - Raw byte
//   - xterm modifyOtherKeys: ESC[27;5;{keyCode}~
//   - CSI u (kitty keyboard protocol): ESC[{keyCode};5u
func IndexDetachKey(data []byte, detachByte byte) int {
	if idx := bytes.IndexByte(data, detachByte); idx >= 0 {
		return idx
	}
	var keyCode byte
	if detachByte >= 1 && detachByte <= 26 {
		keyCode = detachByte + 96
	} else if detachByte >= 28 && detachByte <= 31 {
		keyCode = detachByte + 64
	}
	if keyCode > 0 {
		modSeq := fmt.Sprintf("\x1b[27;5;%d~", keyCode)
		if idx := bytes.Index(data, []byte(modSeq)); idx >= 0 {
			return idx
		}
		csiSeq := fmt.Sprintf("\x1b[%d;5u", keyCode)
		if idx := bytes.Index(data, []byte(csiSeq)); idx >= 0 {
			return idx
		}
	}
	return -1
}

// IndexCtrlQ returns the index of a Ctrl+Q sequence in data, or -1 if not found.
// This is a convenience wrapper around IndexDetachKey with the default Ctrl+Q byte.
func IndexCtrlQ(data []byte) int {
	return IndexDetachKey(data, 17)
}

// Attach attaches to the tmux session with full PTY support.
// The detach key (default Ctrl+Q) will detach and return to the caller.
// An optional detachByte overrides the default detach key (0x11 = Ctrl+Q).
func (s *Session) Attach(ctx context.Context, detachByte ...byte) error {
	detach := byte(17)
	if len(detachByte) > 0 && detachByte[0] != 0 {
		detach = detachByte[0]
	}

	if !s.Exists() {
		return fmt.Errorf("session %s does not exist", s.Name)
	}

	// Create context with cancel for detach key
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Start tmux attach command with PTY
	cmd := exec.CommandContext(ctx, "tmux", "attach-session", "-t", s.Name)

	// Start command with PTY
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("failed to start pty: %w", err)
	}
	defer ptmx.Close()

	// Set the PTY to raw mode so all bytes pass through transparently.
	// Without this, the PTY's default terminal settings (ISIG enabled)
	// interpret Ctrl+Z as SUSP and send SIGTSTP to the tmux attach process,
	// causing it to exit and returning the user to the session list.
	if _, err := term.MakeRaw(int(ptmx.Fd())); err != nil {
		return fmt.Errorf("failed to set pty raw mode: %w", err)
	}

	// Save original terminal state and set raw mode
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("failed to set raw mode: %w", err)
	}
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }()

	// Handle window resize signals
	sigwinch := make(chan os.Signal, 1)
	signal.Notify(sigwinch, syscall.SIGWINCH)
	sigwinchDone := make(chan struct{}) // Signal for SIGWINCH goroutine to exit
	defer func() {
		signal.Stop(sigwinch)
		close(sigwinchDone) // Signal goroutine to exit
		// Don't close sigwinch - signal.Stop() handles cleanup
	}()

	// WaitGroup to track ALL goroutines (including SIGWINCH handler)
	var wg sync.WaitGroup

	// SIGWINCH handler goroutine - properly tracked in WaitGroup
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
				if ws, err := pty.GetsizeFull(os.Stdin); err == nil {
					_ = pty.Setsize(ptmx, ws)
				}
			}
		}
	}()
	// Initial resize
	sigwinch <- syscall.SIGWINCH

	// Channel to signal detach via configured detach key
	detachCh := make(chan struct{})

	// Channel for I/O errors (buffered to prevent goroutine leaks)
	ioErrors := make(chan error, 2)

	// Timeout to ignore initial terminal control sequences (50ms)
	startTime := time.Now()
	const controlSeqTimeout = 50 * time.Millisecond
	const terminalStyleReset = "\x1b]8;;\x1b\\\x1b[0m\x1b[24m\x1b[39m\x1b[49m"
	outputDone := make(chan struct{})

	// Goroutine 1: Copy PTY output to stdout
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(outputDone)
		_, err := io.Copy(os.Stdout, ptmx)
		if err != nil && err != io.EOF {
			// Only report non-EOF errors (EOF is normal on PTY close)
			select {
			case ioErrors <- fmt.Errorf("PTY read error: %w", err):
			default:
				// Channel full, error already reported
			}
		}
	}()

	// Goroutine 2: Read stdin, intercept detach key, forward rest to PTY
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 32)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				if err == io.EOF {
					break
				}
				// Report stdin read error
				select {
				case ioErrors <- fmt.Errorf("stdin read error: %w", err):
				default:
				}
				return
			}

			// Discard initial terminal control sequences (within first 50ms)
			// These are things like terminal capability queries
			if time.Since(startTime) < controlSeqTimeout {
				continue
			}

			// Check for the detach key anywhere in the input chunk.
			// Some terminals coalesce reads, so detach must not require a single-byte read.
			// Handles raw byte, xterm modifyOtherKeys, and kitty CSI u encodings.
			if idx := IndexDetachKey(buf[:n], detach); idx >= 0 {
				// Forward any bytes before the detach key, then detach.
				if idx > 0 {
					if _, err := ptmx.Write(buf[:idx]); err != nil {
						select {
						case ioErrors <- fmt.Errorf("PTY write error: %w", err):
						default:
						}
						return
					}
				}
				close(detachCh)
				cancel()
				return
			}

			// Forward other input to tmux PTY
			if _, err := ptmx.Write(buf[:n]); err != nil {
				// Report PTY write error
				select {
				case ioErrors <- fmt.Errorf("PTY write error: %w", err):
				default:
				}
				return
			}
		}
	}()

	// Wait for command to finish - tracked in WaitGroup
	cmdDone := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		cmdDone <- cmd.Wait()
	}()

	// Ensures we don't return to Bubble Tea while PTY output is still being written.
	// This avoids terminal style leakage (for example underline/hyperlink state)
	// from the attached client into the Agent Deck UI.
	cleanupAttach := func() {
		cancel()
		_ = ptmx.Close()
		select {
		case <-outputDone:
		case <-time.After(20 * time.Millisecond):
		}
		// Reset OSC-8 hyperlink state + SGR attributes before Bubble Tea redraws.
		_, _ = os.Stdout.WriteString(terminalStyleReset)
		// Re-disable Kitty keyboard protocol — the attached session (or tools
		// inside it like Claude Code) may have re-enabled it, leaving the
		// outer terminal in CSI u mode where Bubble Tea can't parse shift keys.
		terminal.DisableKittyKeyboard(os.Stdout)
	}

	// Wait for either detach key or command completion
	var attachErr error
	select {
	case <-detachCh:
		// User pressed detach key, detach gracefully
		attachErr = nil
	case err := <-cmdDone:
		if err != nil {
			// Check if it's a normal exit (tmux detach via Ctrl+B,D)
			if exitErr, ok := err.(*exec.ExitError); ok {
				if exitErr.ExitCode() == 0 || exitErr.ExitCode() == 1 {
					attachErr = nil
				} else {
					attachErr = err
				}
			} else {
				attachErr = err
			}
			// Context cancelled is normal (from detach key)
			if ctx.Err() != nil {
				attachErr = nil
			}
		} else {
			attachErr = nil
		}
	case <-ctx.Done():
		attachErr = nil
	}

	cleanupAttach()
	return attachErr
}

// AttachWindow attaches to a specific window within this tmux session.
// Selects the target window first, then uses the standard Attach flow.
// An optional detachByte overrides the default detach key.
func (s *Session) AttachWindow(ctx context.Context, windowIndex int, detachByte ...byte) error {
	if !s.Exists() {
		return fmt.Errorf("session %s does not exist", s.Name)
	}

	// Select the target window before attaching
	target := fmt.Sprintf("%s:%d", s.Name, windowIndex)
	if err := exec.Command("tmux", "select-window", "-t", target).Run(); err != nil {
		return fmt.Errorf("failed to select window %s: %w", target, err)
	}

	return s.Attach(ctx, detachByte...)
}

// Resize changes the terminal size of the tmux session
func (s *Session) Resize(cols, rows int) error {
	// Resize the tmux window
	cmd := exec.Command("tmux", "resize-window", "-t", s.Name, "-x", fmt.Sprintf("%d", cols), "-y", fmt.Sprintf("%d", rows))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to resize window: %w", err)
	}
	return nil
}

// AttachReadOnly attaches to the session in read-only mode
func (s *Session) AttachReadOnly(ctx context.Context) error {
	if !s.Exists() {
		return fmt.Errorf("session %s does not exist", s.Name)
	}

	// Save original terminal state
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("failed to set raw mode: %w", err)
	}
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }()

	// Start tmux attach command in read-only mode
	cmd := exec.CommandContext(ctx, "tmux", "attach-session", "-r", "-t", s.Name)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Start the attach command
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to attach to session: %w", err)
	}

	// Wait for command to finish
	if err := cmd.Wait(); err != nil {
		// Check if it's a normal detach
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() == 0 || exitErr.ExitCode() == 1 {
				return nil
			}
		}
		return fmt.Errorf("attach command failed: %w", err)
	}

	return nil
}

// StreamOutput streams the session output to the provided writer
func (s *Session) StreamOutput(ctx context.Context, w io.Writer) error {
	if !s.Exists() {
		return fmt.Errorf("session %s does not exist", s.Name)
	}

	// Use tmux pipe-pane to stream output
	cmd := exec.CommandContext(ctx, "tmux", "pipe-pane", "-t", s.Name, "-o", "cat")
	cmd.Stdout = w
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start pipe-pane: %w", err)
	}

	// Wait for context cancellation or command completion
	// Use WaitGroup to prevent goroutine leak on context cancellation
	var wg sync.WaitGroup
	errChan := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		errChan <- cmd.Wait()
	}()

	select {
	case <-ctx.Done():
		// Stop pipe-pane - error is intentionally ignored since we're
		// already returning ctx.Err() and cleanup failure is non-fatal
		stopCmd := exec.Command("tmux", "pipe-pane", "-t", s.Name)
		_ = stopCmd.Run()
		// Wait for the goroutine to complete before returning
		wg.Wait()
		return ctx.Err()
	case err := <-errChan:
		if err != nil {
			return fmt.Errorf("pipe-pane failed: %w", err)
		}
		return nil
	}
}
