package tmux

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	// be in workdir.
	out, err = exec.Command("tmux", "-L", socket, "display-message", "-p", "-t", target, "#{pane_current_path}").CombinedOutput()
	if err != nil {
		t.Fatalf("display-message: %v: %s", err, out)
	}
	got, err := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("EvalSymlinks(pane_current_path): %v", err)
	}
	if got != workdir {
		t.Errorf("active pane path = %q, want %q", got, workdir)
	}
}
