//go:build !windows

package tmux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// Regression tests for the rc.5 remote-parity defect: attaching to a session
// whose tmux server logged a config error (g14: `~/.tmux.conf:25: invalid
// option: extended-keys-format`) left the attach view "frozen" on the warning
// line. tmux defers config-error display until the first client attaches, then
// switches the session's active pane into view-mode (window name "[tmux]",
// `[1/1]` marker) to show the errors. Every key after that goes to the mode's
// key table, not the agent, and the pane stays in that mode across detaches
// until someone presses `q`. The attach path must dismiss that view so the
// agent pane is live from the first frame.

// badTmuxConfig writes a config with one invalid option so the server records
// a config cause exactly the way g14's .tmux.conf did.
func badTmuxConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bad.tmux.conf")
	if err := os.WriteFile(path, []byte("set -g bogus-option-for-test 1\n"), 0o600); err != nil {
		t.Fatalf("write bad tmux config: %v", err)
	}
	return path
}

// newSessionWithConfig starts an isolated tmux server from the given config
// file and returns a Session bound to its socket plus a tmux helper.
func newSessionWithConfig(t *testing.T, name, config string) (*Session, func(args ...string) string) {
	t.Helper()
	skipIfNoTmuxBinary(t)
	// TestMain isolated TMUX_TMPDIR, so a -L name never reaches a live server.
	socket := fmt.Sprintf("cfgerr-%d-%s", os.Getpid(), name)
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })

	run := func(args ...string) string {
		t.Helper()
		full := append([]string{"-L", socket}, args...)
		out, err := exec.Command("tmux", full...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	spawn := []string{"-L", socket}
	if config != "" {
		spawn = append(spawn, "-f", config)
	}
	spawn = append(spawn, "new-session", "-d", "-x", "80", "-y", "24", "-s", name, "sleep 300")
	if out, err := exec.Command("tmux", spawn...).CombinedOutput(); err != nil {
		t.Fatalf("spawn tmux server: %v\n%s", err, out)
	}
	return &Session{Name: name, SocketName: socket}, run
}

// attachClientPTY attaches a real tmux client on a PTY, which is what makes
// tmux flush pending config causes into the pane.
func attachClientPTY(t *testing.T, s *Session) {
	t.Helper()
	cmd := exec.Command("tmux", "-L", s.SocketName, "attach-session", "-t", s.Name)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("attach client pty: %v", err)
	}
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := ptmx.Read(buf); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = ptmx.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})
}

const (
	paneModeFormat       = "#{pane_in_mode}|#{pane_mode}"
	paneModeWindowFormat = "#{pane_in_mode}|#{pane_mode}|#{window_name}"
)

func display(t *testing.T, run func(...string) string, name, format string) string {
	t.Helper()
	return run("display-message", "-p", "-t", name, format)
}

// waitDisplay polls display-message with the format until it prints want and
// returns the last value seen. Callers pass paneModeFormat when waiting for a
// pane to leave a mode: tmux renames the window back asynchronously, so the
// window name is not part of that condition.
func waitDisplay(t *testing.T, run func(...string) string, name, format, want string) string {
	t.Helper()
	var got string
	waitFor(3*time.Second, func() bool {
		got = display(t, run, name, format)
		return got == want
	})
	return got
}

func waitAttached(t *testing.T, run func(...string) string, name string) {
	t.Helper()
	if !waitFor(3*time.Second, func() bool { return display(t, run, name, "#{session_attached}") != "0" }) {
		t.Fatal("client never attached")
	}
}

func TestDismissConfigErrorView_CancelsViewModeAfterFirstAttach(t *testing.T) {
	name := fmt.Sprintf("cfgerr-%d", os.Getpid())
	sess, run := newSessionWithConfig(t, name, badTmuxConfig(t))

	// tmux 3.4+ defers config-error display until the first attach; tmux 3.3
	// (the g14 test image) enters view-mode as soon as the session exists.
	// Either is a valid starting point; the attach below must end in the
	// config-error view-mode that DismissConfigErrorView has to cancel.
	if got := display(t, run, name, paneModeFormat); got != "0|" && got != "1|view-mode" {
		t.Fatalf("pane before any client attaches = %q, want clean or config-error view-mode", got)
	}
	attachClientPTY(t, sess)
	// Reproduction of the g14 frame: the pane is now in view-mode and the
	// window is renamed "[tmux]" (the `0:[tmux]*` in the status bar).
	if got := waitDisplay(t, run, name, paneModeWindowFormat, "1|view-mode|[tmux]"); got != "1|view-mode|[tmux]" {
		t.Fatalf("tmux did not put the pane into config-error view-mode on first attach, got %q", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !sess.DismissConfigErrorView(ctx, configErrorViewWindow) {
		t.Fatal("DismissConfigErrorView reported nothing dismissed while the pane was in view-mode")
	}
	if got := waitDisplay(t, run, name, paneModeFormat, "0|"); got != "0|" {
		t.Fatalf("pane still stuck after dismiss, got %q", got)
	}
}

func TestDismissConfigErrorView_NoopWhenConfigIsClean(t *testing.T) {
	name := fmt.Sprintf("cfgclean-%d", os.Getpid())
	sess, run := newSessionWithConfig(t, name, "")
	attachClientPTY(t, sess)
	waitAttached(t, run, name)

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if sess.DismissConfigErrorView(ctx, configErrorViewWindow) {
		t.Fatal("DismissConfigErrorView cancelled a mode on a clean server")
	}
	// A clean, attached server must not pay the full watchdog window.
	if elapsed := time.Since(start); elapsed >= configErrorViewWindow {
		t.Fatalf("clean attach waited the whole window (%s)", elapsed)
	}
	if got := display(t, run, name, paneModeFormat); got != "0|" {
		t.Fatalf("clean pane changed state: %q", got)
	}
}

// A user scrolling back in copy-mode from another client must not be kicked
// out: only tmux's own view-mode (config causes, show-messages) is dismissed.
func TestDismissConfigErrorView_LeavesUserCopyModeAlone(t *testing.T) {
	name := fmt.Sprintf("cfgcopy-%d", os.Getpid())
	sess, run := newSessionWithConfig(t, name, "")
	attachClientPTY(t, sess)
	waitAttached(t, run, name)
	run("copy-mode", "-t", name)
	if got := waitDisplay(t, run, name, paneModeWindowFormat, "1|copy-mode|[tmux]"); got != "1|copy-mode|[tmux]" {
		t.Fatalf("could not enter copy-mode for the test, got %q", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if sess.DismissConfigErrorView(ctx, configErrorViewWindow) {
		t.Fatal("DismissConfigErrorView cancelled the user's copy-mode")
	}
	if got := display(t, run, name, paneModeWindowFormat); got != "1|copy-mode|[tmux]" {
		t.Fatalf("copy-mode was disturbed: %q", got)
	}
}
