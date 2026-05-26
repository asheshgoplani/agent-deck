//go:build !windows
// +build !windows

package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
//
// The two width-arbitration tests (full-width + narrow-terminal) live in
// issue1167_attach_width_timing_test.go behind the `tmux_timing` build tag.
// They depend on the tmux server completing client window-size arbitration,
// which is CPU-starved past any short deadline in the contended release
// `go test -race ./...` run on a 4-vCPU runner (see #1167 investigation). They
// run isolated, with `-p 1`, in their own CI job instead of the full suite.
// The fast failure-mode test below has no such dependency, so it stays in the
// default build and keeps PR coverage of the size-probe fallback path.

func tmuxCtl1167(t *testing.T, socket string, args ...string) {
	t.Helper()
	full := append([]string{"-S", socket}, args...)
	if out, err := exec.Command("tmux", full...).CombinedOutput(); err != nil {
		t.Fatalf("tmux %v: %v\n%s", args, err, out)
	}
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
