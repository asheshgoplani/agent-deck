//go:build !windows
// +build !windows

package session

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/creack/pty"
)

// TestRemoteAttach_FullWidthFromFrameOne is the remote-surface (#1167) parity
// test. The remote attach path (SSHRunner.Attach in ssh.go) now starts its
// local PTY through the shared tmux.StartAttachPTY helper rather than a bare
// pty.Start. This test exercises that exact dependency: with a wide controlling
// terminal, the attached tmux client must size the window to the full terminal
// width, not the 80-col default that produced the ~50% symptom.
//
// A full SSH round-trip needs a live remote host, so this covers the sizing
// dependency the remote path relies on. The local TUI path is covered by
// internal/tmux/issue1167_attach_width_test.go.
func TestRemoteAttach_FullWidthFromFrameOne(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux binary not available")
	}
	const cols, rows uint16 = 200, 50
	const name = "issue1167-remote-fullwidth"
	socket := filepath.Join(t.TempDir(), "sock")

	// Reproduce a remote session's birth: detached (default 80x24),
	// window-size=largest + aggressive-resize=on.
	run := func(args ...string) {
		t.Helper()
		full := append([]string{"-S", socket}, args...)
		if out, err := exec.Command("tmux", full...).CombinedOutput(); err != nil {
			t.Fatalf("tmux %v: %v\n%s", args, err, out)
		}
	}
	run("new-session", "-d", "-s", name)
	run("set-option", "-t", name, "window-size", "largest")
	run("set-window-option", "-t", name, "aggressive-resize", "on")
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", socket, "kill-server").Run() })

	termPTY, termTTY, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer func() { _ = termPTY.Close(); _ = termTTY.Close() }()
	if err := pty.Setsize(termPTY, &pty.Winsize{Cols: cols, Rows: rows}); err != nil {
		t.Fatalf("Setsize: %v", err)
	}

	cmd := exec.Command("tmux", "-S", socket, "attach-session", "-t", name)
	ptmx, err := tmux.StartAttachPTY(cmd, termTTY)
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

	time.Sleep(200 * time.Millisecond)

	out, err := exec.Command("tmux", "-S", socket,
		"display", "-p", "-t", name, "#{window_width}").CombinedOutput()
	if err != nil {
		t.Fatalf("display window_width: %v\n%s", err, out)
	}
	got, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("parse window_width %q: %v", out, err)
	}
	if got != int(cols) {
		t.Fatalf("remote attach window width = %d, want %d (full terminal); #1167", got, cols)
	}
}
