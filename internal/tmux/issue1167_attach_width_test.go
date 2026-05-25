//go:build !windows
// +build !windows

package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// Regression tests for #1167: opening/attaching a claude session renders the
// pane at ~50% of the terminal width instead of 100%.
//
// Root cause: a detached `tmux new-session` (no -x/-y) is born at tmux's
// default-size (80x24). When agent-deck attaches via the bare pty.Start, the
// attach client's PTY is *also* created at the 80x24 default, so tmux's
// window-size=largest pins the window to 80 cols — ~half of a wide terminal —
// until an async SIGWINCH grows it. StartAttachPTY pre-sizes the attach PTY to
// the controlling terminal so the client connects full-width from frame one.

func tmuxCtl1167(t *testing.T, socket string, args ...string) {
	t.Helper()
	full := append([]string{"-S", socket}, args...)
	if out, err := exec.Command("tmux", full...).CombinedOutput(); err != nil {
		t.Fatalf("tmux %v: %v\n%s", args, err, out)
	}
}

func windowWidth1167(t *testing.T, socket, name string) int {
	t.Helper()
	out, err := exec.Command("tmux", "-S", socket,
		"display", "-p", "-t", name, "#{window_width}").CombinedOutput()
	if err != nil {
		t.Fatalf("display window_width: %v\n%s", err, out)
	}
	w, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("parse window_width %q: %v", out, err)
	}
	return w
}

// newDetachedSession1167 reproduces production session birth: a detached
// session with NO -x/-y (so tmux uses its 80x24 default-size) plus the
// window-size=largest / aggressive-resize=on options Session.Start pins.
func newDetachedSession1167(t *testing.T, name string) string {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux binary not available")
	}
	socket := filepath.Join(t.TempDir(), "sock")
	tmuxCtl1167(t, socket, "new-session", "-d", "-s", name)
	tmuxCtl1167(t, socket, "set-option", "-t", name, "window-size", "largest")
	tmuxCtl1167(t, socket, "set-window-option", "-t", name, "aggressive-resize", "on")
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", socket, "kill-server").Run() })
	return socket
}

// attachAt1167 opens a controlling terminal of the given size, attaches through
// StartAttachPTY, waits for tmux to register the client, and returns the window
// width tmux reports.
func attachAt1167(t *testing.T, socket, name string, cols, rows uint16) int {
	t.Helper()

	termPTY, termTTY, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer func() { _ = termPTY.Close(); _ = termTTY.Close() }()
	if err := pty.Setsize(termPTY, &pty.Winsize{Cols: cols, Rows: rows}); err != nil {
		t.Fatalf("Setsize: %v", err)
	}

	cmd := exec.Command("tmux", "-S", socket, "attach-session", "-t", name)
	ptmx, err := StartAttachPTY(cmd, termTTY)
	if err != nil {
		t.Fatalf("StartAttachPTY: %v", err)
	}
	defer func() {
		_ = ptmx.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	}()

	// Give tmux a beat to register the client and arbitrate window-size.
	time.Sleep(200 * time.Millisecond)
	return windowWidth1167(t, socket, name)
}

// TestStartAttachPTY_FullWidthFromFrameOne is the happy path: attaching with a
// wide controlling terminal must grow the window to the full terminal width,
// not leave it at the 80-col default that produced the ~50% symptom.
func TestStartAttachPTY_FullWidthFromFrameOne(t *testing.T) {
	const cols, rows uint16 = 200, 50
	socket := newDetachedSession1167(t, "issue1167-fullwidth")

	got := attachAt1167(t, socket, "issue1167-fullwidth", cols, rows)
	if got != int(cols) {
		t.Fatalf("attached window width = %d, want %d (full terminal). "+
			"#1167: the pane renders at ~50%% because the attach PTY started at "+
			"the 80-col default instead of the controlling terminal size", got, cols)
	}
}

// TestStartAttachPTY_MatchesNarrowTerminal is the boundary case: the window must
// track the *actual* terminal size, proving StartAttachPTY uses the real
// dimensions rather than any hardcoded width.
func TestStartAttachPTY_MatchesNarrowTerminal(t *testing.T) {
	const cols, rows uint16 = 132, 30
	socket := newDetachedSession1167(t, "issue1167-narrow")

	got := attachAt1167(t, socket, "issue1167-narrow", cols, rows)
	if got != int(cols) {
		t.Fatalf("attached window width = %d, want %d (exact terminal size)", got, cols)
	}
}

// TestStartAttachPTY_FallsBackWhenSizeUnavailable is the failure mode: when the
// controlling fd is not a terminal (GetsizeFull fails), StartAttachPTY must
// still start the PTY (degraded, default size) rather than erroring out — the
// attach must never break just because the size probe failed.
func TestStartAttachPTY_FallsBackWhenSizeUnavailable(t *testing.T) {
	socket := newDetachedSession1167(t, "issue1167-fallback")

	// An os.Pipe read end is a valid *os.File but not a tty, so GetsizeFull
	// returns an error and the helper must fall back to a plain start.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()

	cmd := exec.Command("tmux", "-S", socket, "attach-session", "-t", "issue1167-fallback")
	ptmx, err := StartAttachPTY(cmd, r)
	if err != nil {
		t.Fatalf("StartAttachPTY must not fail when size is unavailable: %v", err)
	}
	if ptmx == nil {
		t.Fatal("StartAttachPTY returned a nil PTY on fallback")
	}
	_ = ptmx.Close()
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
}
