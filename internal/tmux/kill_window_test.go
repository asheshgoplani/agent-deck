package tmux

import (
	"errors"
	"fmt"
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
	windowID, err := s.WindowID(newIndex)
	if err != nil {
		t.Fatalf("WindowID: %v", err)
	}
	if err := s.KillWindow(newIndex, windowID); err != nil {
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

// soleWindowIndex returns the index of the (assumed only) window in target,
// without assuming a particular tmux base-index configuration.
func soleWindowIndex(t *testing.T, socket, target string) int {
	t.Helper()
	out, err := exec.Command("tmux", "-L", socket, "list-windows", "-t", target, "-F", "#{window_index}").CombinedOutput()
	if err != nil {
		t.Fatalf("list-windows: %v: %s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly one window, got %v", lines)
	}
	index, err := strconv.Atoi(lines[0])
	if err != nil {
		t.Fatalf("parse window index %q: %v", lines[0], err)
	}
	return index
}

// TestSession_KillWindow_RefusesLastWindow verifies that KillWindow returns
// ErrLastWindow instead of killing the session's only remaining window
// (which would take the whole session down).
func TestSession_KillWindow_RefusesLastWindow(t *testing.T) {
	requireTmux(t)
	socket, target := makeIsolatedServer(t)
	onlyIndex := soleWindowIndex(t, socket, target)

	s := &Session{Name: target, SocketName: socket}
	windowID, err := s.WindowID(onlyIndex)
	if err != nil {
		t.Fatalf("WindowID: %v", err)
	}
	if err := s.KillWindow(onlyIndex, windowID); !errors.Is(err, ErrLastWindow) {
		t.Fatalf("KillWindow on the last window = %v, want ErrLastWindow", err)
	}

	out, err := exec.Command("tmux", "-L", socket, "list-windows", "-t", target, "-F", "#{window_index}").CombinedOutput()
	if err != nil {
		t.Fatalf("list-windows after refused kill: %v: %s", err, out)
	}
	if got := len(strings.Split(strings.TrimSpace(string(out)), "\n")); got != 1 {
		t.Fatalf("window count = %d, want 1 (the last window must survive)", got)
	}
}

// TestSession_KillWindow_RefusesReorderedWindow verifies the identity guard:
// if the window originally at an index closes and a different window slides
// into (or is created at) that same index before the confirmed kill runs,
// KillWindow must refuse instead of killing whatever now sits at the stale
// index. This is the liveness-is-not-identity case — the index alone is not
// a stable reference to "the window the user selected".
func TestSession_KillWindow_RefusesReorderedWindow(t *testing.T) {
	requireTmux(t)
	socket, target := makeIsolatedServer(t)

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
	staleIndex, err := strconv.Atoi(windows[1])
	if err != nil {
		t.Fatalf("parse window index %q: %v", windows[1], err)
	}

	s := &Session{Name: target, SocketName: socket}
	// This is the id a confirm dialog would have captured "at prompt time",
	// for the window that is about to disappear from staleIndex.
	staleWindowID, err := s.WindowID(staleIndex)
	if err != nil {
		t.Fatalf("WindowID: %v", err)
	}

	// Simulate the window at staleIndex closing on its own, and a different
	// window taking its place at the same index — outside of agent-deck, so
	// KillWindow only learns about it through the atomic id check.
	if out, err := exec.Command("tmux", "-L", socket, "kill-window", "-t", fmt.Sprintf("%s:%d", target, staleIndex)).CombinedOutput(); err != nil {
		t.Fatalf("kill-window (setup): %v: %s", err, out)
	}
	if out, err := exec.Command("tmux", "-L", socket, "new-window", "-t", fmt.Sprintf("%s:%d", target, staleIndex), "sleep", "60").CombinedOutput(); err != nil {
		t.Fatalf("new-window at stale index (setup): %v: %s", err, out)
	}
	newWindowID, err := s.WindowID(staleIndex)
	if err != nil {
		t.Fatalf("WindowID (new window): %v", err)
	}
	if newWindowID == staleWindowID {
		t.Fatalf("setup did not actually replace the window at index %d", staleIndex)
	}

	if err := s.KillWindow(staleIndex, staleWindowID); !errors.Is(err, ErrWindowChanged) {
		t.Fatalf("KillWindow with a stale window id = %v, want ErrWindowChanged", err)
	}

	// The new (different) window at that index must survive untouched.
	windows = listWindows()
	if len(windows) != 2 {
		t.Fatalf("window count after refused kill = %d, want 2 (windows: %v)", len(windows), windows)
	}
	survivingID, err := s.WindowID(staleIndex)
	if err != nil {
		t.Fatalf("WindowID after refused kill: %v", err)
	}
	if survivingID != newWindowID {
		t.Fatalf("window at index %d changed after refused kill: got %s, want %s", staleIndex, survivingID, newWindowID)
	}
}

// TestSession_WindowID verifies WindowID returns the tmux-generated id
// ("@<digits>") for the window at the given index.
func TestSession_WindowID(t *testing.T) {
	requireTmux(t)
	socket, target := makeIsolatedServer(t)

	onlyIndex := soleWindowIndex(t, socket, target)
	s := &Session{Name: target, SocketName: socket}
	id, err := s.WindowID(onlyIndex)
	if err != nil {
		t.Fatalf("WindowID: %v", err)
	}
	if !strings.HasPrefix(id, "@") {
		t.Errorf("WindowID = %q, want a tmux window id starting with '@'", id)
	}

	if _, err := s.WindowID(99); err == nil {
		t.Error("WindowID on a nonexistent window index should return an error")
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
