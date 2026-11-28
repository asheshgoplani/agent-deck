package tmux

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSession_NewShellWindow verifies that NewShellWindow adds a second
// window to the session (instead of splitting the current one) and that the
// new window's pane starts in the requested workdir.
func TestSession_NewShellWindow(t *testing.T) {
	requireTmux(t)
	socket, target := makeIsolatedServer(t)

	workdir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	s := &Session{Name: target, SocketName: socket}
	if err := s.NewShellWindow(workdir); err != nil {
		t.Fatalf("NewShellWindow: %v", err)
	}

	out, err := exec.Command("tmux", "-L", socket, "list-windows", "-t", target, "-F", "#{window_index}").CombinedOutput()
	if err != nil {
		t.Fatalf("list-windows: %v: %s", err, out)
	}
	windows := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(windows) != 2 {
		t.Fatalf("window count = %d, want 2 (windows: %v)", len(windows), windows)
	}

	// new-window selects the new window, so the session's active pane must
	// end up in workdir. new-window returns once tmux has forked the pane
	// process, before that child has chdir'd, so pane_current_path can still
	// report the tmux server's cwd for a moment (seen on the release
	// integration job under -race). Poll until it settles or a deadline hits.
	var got string
	deadline := time.Now().Add(10 * time.Second)
	for {
		out, err = exec.Command("tmux", "-L", socket, "display-message", "-p", "-t", target, "#{pane_current_path}").CombinedOutput()
		if err != nil {
			t.Fatalf("display-message: %v: %s", err, out)
		}
		got, err = filepath.EvalSymlinks(strings.TrimSpace(string(out)))
		if err == nil && got == workdir {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("active pane path = %q (err %v), want %q after 10s", got, err, workdir)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
