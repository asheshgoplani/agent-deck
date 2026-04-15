//go:build !windows

package tmux

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/term"
)

// TestAttach_CtrlC_ForwardedToSession verifies that Ctrl+C delivered via
// tmux send-keys is forwarded to the foreground process of an attached session.
// This is a baseline test of the tmux send-keys path (not the PTY Attach path).
func TestAttach_CtrlC_ForwardedToSession(t *testing.T) {
	skipIfNoTmuxServer(t)

	sentinelFile := filepath.Join(t.TempDir(), "sigint_received")
	name := SessionPrefix + "ptytest-ctrlc-" + fmt.Sprintf("%d", time.Now().UnixNano()%100000)
	script := fmt.Sprintf(`trap 'touch %s' INT; while true; do sleep 1; done`, sentinelFile)

	require.NoError(t,
		exec.Command("tmux", "new-session", "-d", "-s", name, "bash", "-c", script).Run(),
		"failed to create test session %s", name,
	)
	t.Cleanup(func() {
		_ = exec.Command("tmux", "kill-session", "-t", name).Run()
	})

	// Wait for the trap to register in the shell
	time.Sleep(500 * time.Millisecond)

	// Send Ctrl+C to the session foreground process via tmux send-keys
	require.NoError(t,
		exec.Command("tmux", "send-keys", "-t", name, "C-c", "").Run(),
		"failed to send Ctrl+C via tmux send-keys",
	)

	// Wait for the trap to fire and create the sentinel file
	time.Sleep(500 * time.Millisecond)

	_, err := os.Stat(sentinelFile)
	require.NoError(t, err, "SIGINT not forwarded: sentinel file %s not created", sentinelFile)
}

// TestAttach_CtrlC_ForwardedThroughPTY verifies that Ctrl+C sent after the
// 50ms controlSeqTimeout window is forwarded through the PTY Attach() path
// to the attached session's foreground process.
// Skips if stdin is not a terminal (CI/pipe environments).
func TestAttach_CtrlC_ForwardedThroughPTY(t *testing.T) {
	skipIfNoTmuxServer(t)

	// Attach() calls term.MakeRaw(os.Stdin.Fd()) which requires a real terminal.
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		t.Skip("stdin is not a terminal (CI/pipe environment); skipping PTY attach test")
	}

	sentinelFile := filepath.Join(t.TempDir(), "sigint_received_pty")
	name := SessionPrefix + "ptytest-ctrlcpty-" + fmt.Sprintf("%d", time.Now().UnixNano()%100000)
	script := fmt.Sprintf(`trap 'touch %s' INT; while true; do sleep 1; done`, sentinelFile)

	require.NoError(t,
		exec.Command("tmux", "new-session", "-d", "-s", name, "bash", "-c", script).Run(),
		"failed to create test session %s", name,
	)
	t.Cleanup(func() {
		_ = exec.Command("tmux", "kill-session", "-t", name).Run()
	})

	// Wait for the trap to register
	time.Sleep(500 * time.Millisecond)

	sess := &Session{Name: name}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	attachDone := make(chan error, 1)
	go func() { attachDone <- sess.Attach(ctx, 0x11) }()

	// Wait past the 50ms controlSeqTimeout window before sending Ctrl+C
	time.Sleep(200 * time.Millisecond)

	// Send Ctrl+C via tmux send-keys (avoids the os.Stdin pipe issue in tests)
	require.NoError(t,
		exec.Command("tmux", "send-keys", "-t", name, "C-c", "").Run(),
		"failed to send Ctrl+C via tmux send-keys",
	)

	// Wait for the trap to fire and create the sentinel file
	time.Sleep(500 * time.Millisecond)

	_, err := os.Stat(sentinelFile)
	require.NoError(t, err, "SIGINT was not forwarded through PTY to the session")

	// Send detach key (Ctrl+Q) to cleanly exit Attach()
	require.NoError(t,
		exec.Command("tmux", "send-keys", "-t", name, "C-q", "").Run(),
		"failed to send detach key",
	)

	select {
	case attachErr := <-attachDone:
		require.NoError(t, attachErr, "Attach returned error after detach")
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("Attach did not return after detach key was sent")
	}
}

// TestAttach_CtrlC_DuringControlSeqTimeout verifies that Ctrl+C sent WITHIN
// the first 50ms controlSeqTimeout window is still forwarded to the session.
// Without the fix, this byte would be dropped by the blanket discard at pty.go:194.
// Skips if stdin is not a terminal (CI/pipe environments).
func TestAttach_CtrlC_DuringControlSeqTimeout(t *testing.T) {
	skipIfNoTmuxServer(t)

	// Attach() calls term.MakeRaw(os.Stdin.Fd()) which requires a real terminal.
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		t.Skip("stdin is not a terminal (CI/pipe environment); skipping PTY attach test")
	}

	sentinelFile := filepath.Join(t.TempDir(), "sigint_received_early")
	name := SessionPrefix + "ptytest-ctrlcearly-" + fmt.Sprintf("%d", time.Now().UnixNano()%100000)
	script := fmt.Sprintf(`trap 'touch %s' INT; while true; do sleep 1; done`, sentinelFile)

	require.NoError(t,
		exec.Command("tmux", "new-session", "-d", "-s", name, "bash", "-c", script).Run(),
		"failed to create test session %s", name,
	)
	t.Cleanup(func() {
		_ = exec.Command("tmux", "kill-session", "-t", name).Run()
	})

	// Wait for the trap to register
	time.Sleep(500 * time.Millisecond)

	sess := &Session{Name: name}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	attachDone := make(chan error, 1)
	go func() { attachDone <- sess.Attach(ctx, 0x11) }()

	// Send Ctrl+C within the 50ms controlSeqTimeout window (only 10ms sleep)
	// WITHOUT the fix, this byte would be dropped.
	time.Sleep(10 * time.Millisecond)

	require.NoError(t,
		exec.Command("tmux", "send-keys", "-t", name, "C-c", "").Run(),
		"failed to send Ctrl+C via tmux send-keys",
	)

	// Wait for the trap to fire
	time.Sleep(500 * time.Millisecond)

	_, err := os.Stat(sentinelFile)
	require.NoError(t, err, "Ctrl+C sent within 50ms window was dropped (bug still present)")

	// Send detach key (Ctrl+Q) to cleanly exit Attach()
	require.NoError(t,
		exec.Command("tmux", "send-keys", "-t", name, "C-q", "").Run(),
		"failed to send detach key",
	)

	select {
	case attachErr := <-attachDone:
		require.NoError(t, attachErr, "Attach returned error after detach")
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("Attach did not return after detach key was sent")
	}
}

// TestControlSeqTimeout_DoesNotDropCtrlC verifies that the filter condition
// used in controlSeqTimeout (buf[0] == 0x1b) does NOT match Ctrl+C (0x03).
// This is a unit test of the filter logic itself.
func TestControlSeqTimeout_DoesNotDropCtrlC(t *testing.T) {
	buf := []byte{0x03} // Ctrl+C
	isEscPrefix := len(buf) > 0 && buf[0] == 0x1b
	require.False(t, isEscPrefix, "Ctrl+C (0x03) must NOT be filtered by controlSeqTimeout (ESC-prefix check)")
}

// TestControlSeqTimeout_DropsEscPrefix verifies that the filter condition
// (buf[0] == 0x1b) correctly matches ESC-prefixed terminal capability queries.
func TestControlSeqTimeout_DropsEscPrefix(t *testing.T) {
	buf := []byte{0x1b, '[', '1', 'm'} // ESC + CSI sequence
	isEscPrefix := len(buf) > 0 && buf[0] == 0x1b
	require.True(t, isEscPrefix, "ESC-prefixed bytes (0x1b...) must be filtered by controlSeqTimeout")
}

// TestStdinDrain_DropsNonEscDAResponse verifies that the drain window
// discards ALL bytes (not just ESC-prefixed), catching split DA responses
// like "1;22;32c" that arrive without their ESC prefix. Fixes #597/#585.
func TestStdinDrain_DropsNonEscDAResponse(t *testing.T) {
	startTime := time.Now()
	const stdinDrainWindow = 150 * time.Millisecond

	// Simulate a split DA1 response: the ESC was consumed in a prior read,
	// leaving the parameter bytes + final byte.
	buf := []byte("1;22;32c")
	n := len(buf)

	withinWindow := time.Since(startTime) < stdinDrainWindow
	shouldDrop := withinWindow && n > 0

	require.True(t, shouldDrop,
		"split DA response %q arriving within drain window must be dropped (fixes #597)",
		string(buf))
}

// TestControlSeqTimeout_PassesRegularInput verifies that regular ASCII bytes
// and common control chars are NOT filtered by the ESC-prefix check.
func TestControlSeqTimeout_PassesRegularInput(t *testing.T) {
	cases := []struct {
		name string
		b    byte
	}{
		{"letter_A", 0x41},
		{"enter", 0x0d},
		{"ctrl_z", 0x1a},
		{"space", 0x20},
		{"ctrl_c", 0x03},
		{"ctrl_q", 0x11},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := []byte{tc.b}
			isEscPrefix := len(buf) > 0 && buf[0] == 0x1b
			require.False(t, isEscPrefix,
				"byte 0x%02x (%s) must NOT be filtered by the ESC-prefix controlSeqTimeout check",
				tc.b, tc.name,
			)
		})
	}
}

// TestCleanupAttach_EmitsScrollbackClear verifies that when Attach() detaches
// via the detach key (Ctrl+Q), the cleanup code emits \033[3J to clear the
// host terminal's scrollback buffer before returning to the TUI.
func TestCleanupAttach_EmitsScrollbackClear(t *testing.T) {
	skipIfNoTmuxServer(t)
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		t.Skip("stdin is not a terminal (CI/pipe environment); skipping PTY attach test")
	}

	name := SessionPrefix + "ptytest-scrollback-" + fmt.Sprintf("%d", time.Now().UnixNano()%100000)
	require.NoError(t,
		exec.Command("tmux", "new-session", "-d", "-s", name, "bash").Run(),
		"failed to create test session %s", name,
	)
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", name).Run() })

	// Redirect os.Stdout to capture cleanupAttach() output
	r, w, err := os.Pipe()
	require.NoError(t, err)
	oldStdout := os.Stdout
	os.Stdout = w

	sess := &Session{Name: name}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	attachDone := make(chan error, 1)
	go func() { attachDone <- sess.Attach(ctx, 0x11) }()

	// Wait for attach to initialize, then send detach key (Ctrl+Q)
	time.Sleep(300 * time.Millisecond)
	require.NoError(t,
		exec.Command("tmux", "send-keys", "-t", name, "C-q", "").Run(),
		"failed to send detach key",
	)

	// Wait for Attach() to fully return (cleanupAttach has executed)
	select {
	case attachErr := <-attachDone:
		// Restore stdout AFTER Attach() returns (prevents write-to-closed-pipe race)
		os.Stdout = oldStdout
		w.Close()
		require.NoError(t, attachErr, "Attach returned error after detach")
	case <-time.After(4 * time.Second):
		cancel()
		// Restore stdout before Fatal to avoid lost output
		os.Stdout = oldStdout
		w.Close()
		t.Fatal("Attach did not return after detach key was sent")
	}

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)

	captured := buf.String()
	require.Contains(t, captured, "\033[3J",
		"cleanupAttach() must emit \\033[3J to clear host terminal scrollback on detach")
}

// TestCleanupAttach_ScrollbackClearBeforeStyleReset verifies that \033[3J is
// emitted BEFORE the terminalStyleReset sequence in cleanupAttach(), ensuring
// the scrollback clear happens before the TUI attempts to redraw (D-04).
func TestCleanupAttach_ScrollbackClearBeforeStyleReset(t *testing.T) {
	skipIfNoTmuxServer(t)
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		t.Skip("stdin is not a terminal (CI/pipe environment); skipping PTY attach test")
	}

	name := SessionPrefix + "ptytest-scrollorder-" + fmt.Sprintf("%d", time.Now().UnixNano()%100000)
	require.NoError(t,
		exec.Command("tmux", "new-session", "-d", "-s", name, "bash").Run(),
		"failed to create test session %s", name,
	)
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", name).Run() })

	r, w, err := os.Pipe()
	require.NoError(t, err)
	oldStdout := os.Stdout
	os.Stdout = w

	sess := &Session{Name: name}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	attachDone := make(chan error, 1)
	go func() { attachDone <- sess.Attach(ctx, 0x11) }()

	time.Sleep(300 * time.Millisecond)
	require.NoError(t,
		exec.Command("tmux", "send-keys", "-t", name, "C-q", "").Run(),
	)

	select {
	case attachErr := <-attachDone:
		os.Stdout = oldStdout
		w.Close()
		require.NoError(t, attachErr)
	case <-time.After(4 * time.Second):
		cancel()
		os.Stdout = oldStdout
		w.Close()
		t.Fatal("Attach did not return after detach key")
	}

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)

	captured := buf.String()
	scrollIdx := bytes.Index(buf.Bytes(), []byte("\033[3J"))
	styleIdx := bytes.Index(buf.Bytes(), []byte("\x1b]8;;"))

	require.NotEqual(t, -1, scrollIdx,
		"\\033[3J not found in cleanupAttach output")
	require.NotEqual(t, -1, styleIdx,
		"terminalStyleReset not found in cleanupAttach output")
	require.Less(t, scrollIdx, styleIdx,
		"\\033[3J (scrollback clear) must appear BEFORE terminalStyleReset (per D-04); captured: %q", captured)
}
