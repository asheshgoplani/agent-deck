package tmux

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestSession_KillWindow verifies that KillWindow removes the targeted window
// while leaving the rest of the session intact.
func TestSession_KillWindow(t *testing.T) {
	requireTmux(t)
	socket, target := makeIsolatedServer(t)

	// Add a second window directly so the test only exercises KillWindow.
	if out, err := exec.Command("tmux", "-L", socket, "new-window", "-t", target, "sleep", "60").CombinedOutput(); err != nil {
		t.Fatalf("new-window: %v: %s", err, out)
	}

	listWindows := func() []string {
		t.Helper()
		out, err := exec.Command("tmux", "-L", socket, "list-windows", "-t", target, "-F", "#{window_index}").CombinedOutput()
		if err != nil {
			t.Fatalf("list-windows: %v: %s", err, out)
		}
		return strings.Split(strings.TrimSpace(string(out)), "\n")
	}

	windows := listWindows()
	if len(windows) != 2 {
		t.Fatalf("setup: window count = %d, want 2", len(windows))
	}
	newIndex, err := strconv.Atoi(windows[1])
	if err != nil {
		t.Fatalf("parse window index %q: %v", windows[1], err)
	}

	s := &Session{Name: target, SocketName: socket}
	if err := s.KillWindow(newIndex); err != nil {
		t.Fatalf("KillWindow: %v", err)
	}

	windows = listWindows()
	if len(windows) != 1 {
		t.Fatalf("window count after kill = %d, want 1 (windows: %v)", len(windows), windows)
	}
	if windows[0] == strconv.Itoa(newIndex) {
		t.Errorf("killed window %d still present", newIndex)
	}
}

// TestRemoveCachedWindow verifies that RemoveCachedWindow prunes one window
// from the cache so the TUI drops the row immediately instead of waiting for
// the next background refresh.
func TestRemoveCachedWindow(t *testing.T) {
	windowCacheMu.Lock()
	windowCacheData = map[string][]WindowInfo{
		"sess": {{Index: 1, Name: "agent"}, {Index: 2, Name: "shell"}},
	}
	windowCacheTime = time.Now()
	windowCacheMu.Unlock()
	t.Cleanup(func() {
		windowCacheMu.Lock()
		windowCacheData = nil
		windowCacheMu.Unlock()
	})

	RemoveCachedWindow("sess", 2)

	wins := GetCachedWindows("sess")
	if len(wins) != 1 {
		t.Fatalf("cached window count = %d, want 1 (windows: %v)", len(wins), wins)
	}
	if wins[0].Index != 1 {
		t.Errorf("remaining window index = %d, want 1", wins[0].Index)
	}
}
