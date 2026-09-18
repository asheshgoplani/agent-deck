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

// windowsByID lists target's live windows keyed by stable id, with the
// index and name tmux reports right now.
func windowsByID(t *testing.T, socket, target string) map[string]WindowInfo {
	t.Helper()
	out, err := exec.Command("tmux", "-L", socket, "list-windows", "-t", target, "-F", "#{window_index} #{window_id} #{window_name}").CombinedOutput()
	if err != nil {
		t.Fatalf("list-windows: %v: %s", err, out)
	}
	wins := map[string]WindowInfo{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.SplitN(line, " ", 3)
		if len(fields) != 3 {
			t.Fatalf("unexpected list-windows line %q", line)
		}
		idx, err := strconv.Atoi(fields[0])
		if err != nil {
			t.Fatalf("parse window index %q: %v", fields[0], err)
		}
		wins[fields[1]] = WindowInfo{Index: idx, ID: fields[1], Name: fields[2]}
	}
	return wins
}

// newNamedWindow adds a window with an explicit name (which also turns off
// tmux's automatic-rename for it, so the name is stable for the test) and
// returns its stable id.
func newNamedWindow(t *testing.T, socket, target, name string) string {
	t.Helper()
	out, err := exec.Command("tmux", "-L", socket, "new-window", "-d", "-P", "-F", "#{window_id}", "-t", target, "-n", name, "sleep", "60").CombinedOutput()
	if err != nil {
		t.Fatalf("new-window: %v: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestSession_KillWindow verifies that KillWindow removes the targeted window
// while leaving the rest of the session intact.
func TestSession_KillWindow(t *testing.T) {
	requireTmux(t)
	socket, target := makeIsolatedServer(t)

	// Add a second window directly so the test only exercises KillWindow.
	extraID := newNamedWindow(t, socket, target, "extra-shell")
	if got := len(windowsByID(t, socket, target)); got != 2 {
		t.Fatalf("setup: window count = %d, want 2", got)
	}

	s := &Session{Name: target, SocketName: socket}
	if err := s.KillWindow(extraID, "extra-shell"); err != nil {
		t.Fatalf("KillWindow: %v", err)
	}

	wins := windowsByID(t, socket, target)
	if len(wins) != 1 {
		t.Fatalf("window count after kill = %d, want 1 (windows: %v)", len(wins), wins)
	}
	if _, alive := wins[extraID]; alive {
		t.Errorf("killed window %s still present", extraID)
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
	if out, err := exec.Command("tmux", "-L", socket, "rename-window", "-t", fmt.Sprintf("%s:%d", target, onlyIndex), "only").CombinedOutput(); err != nil {
		t.Fatalf("rename-window (setup): %v: %s", err, out)
	}

	s := &Session{Name: target, SocketName: socket}
	windowID, err := s.WindowID(onlyIndex)
	if err != nil {
		t.Fatalf("WindowID: %v", err)
	}
	if err := s.KillWindow(windowID, "only"); !errors.Is(err, ErrLastWindow) {
		t.Fatalf("KillWindow on the last window = %v, want ErrLastWindow", err)
	}

	if got := len(windowsByID(t, socket, target)); got != 1 {
		t.Fatalf("window count = %d, want 1 (the last window must survive)", got)
	}
}

// TestSession_KillWindow_ByIDAfterRenumber verifies KillWindow kills the
// window the caller selected, not whatever now sits at its old index: with
// windows [1] second, [2] third, an external kill of :1 plus renumber moves
// "third" to index 1. Killing by the id captured for "third" must remove
// "third" (now at 1) and leave every other window alone.
func TestSession_KillWindow_ByIDAfterRenumber(t *testing.T) {
	requireTmux(t)
	socket, target := makeIsolatedServer(t)
	secondID := newNamedWindow(t, socket, target, "second")
	thirdID := newNamedWindow(t, socket, target, "third")
	fourthID := newNamedWindow(t, socket, target, "fourth")
	before := windowsByID(t, socket, target)
	if len(before) != 4 {
		t.Fatalf("setup: window count = %d, want 4: %v", len(before), before)
	}

	// Outside agent-deck: the window before "third" closes and tmux
	// renumbers, so "third" and "fourth" each move down one index.
	if out, err := exec.Command("tmux", "-L", socket, "kill-window", "-t", target+":"+secondID).CombinedOutput(); err != nil {
		t.Fatalf("kill-window (setup): %v: %s", err, out)
	}
	if out, err := exec.Command("tmux", "-L", socket, "move-window", "-r", "-t", target).CombinedOutput(); err != nil {
		t.Fatalf("move-window -r (setup): %v: %s", err, out)
	}
	renumbered := windowsByID(t, socket, target)
	if renumbered[thirdID].Index != before[secondID].Index {
		t.Fatalf("setup: expected %q to slide into index %d, got %+v", "third", before[secondID].Index, renumbered)
	}

	s := &Session{Name: target, SocketName: socket}
	if err := s.KillWindow(thirdID, "third"); err != nil {
		t.Fatalf("KillWindow(%s) after renumber: %v", thirdID, err)
	}

	after := windowsByID(t, socket, target)
	if _, alive := after[thirdID]; alive {
		t.Errorf("selected window %s (third) still present after kill", thirdID)
	}
	if _, alive := after[fourthID]; !alive {
		t.Errorf("wrong window died: %s (fourth) is gone; windows: %v", fourthID, after)
	}
	if len(after) != 2 {
		t.Errorf("window count after kill = %d, want 2: %v", len(after), after)
	}
}

// TestSession_KillWindow_RefusesClosedWindow verifies the identity guard: if
// the selected window closed and a different window took its index before
// the confirmed kill runs, KillWindow must refuse (its id is gone from the
// session) instead of killing whatever now sits at the stale index.
func TestSession_KillWindow_RefusesClosedWindow(t *testing.T) {
	requireTmux(t)
	socket, target := makeIsolatedServer(t)
	staleID := newNamedWindow(t, socket, target, "shell")
	staleIndex := windowsByID(t, socket, target)[staleID].Index

	if out, err := exec.Command("tmux", "-L", socket, "kill-window", "-t", target+":"+staleID).CombinedOutput(); err != nil {
		t.Fatalf("kill-window (setup): %v: %s", err, out)
	}
	if out, err := exec.Command("tmux", "-L", socket, "new-window", "-d", "-t", fmt.Sprintf("%s:%d", target, staleIndex), "-n", "shell", "sleep", "60").CombinedOutput(); err != nil {
		t.Fatalf("new-window at stale index (setup): %v: %s", err, out)
	}

	s := &Session{Name: target, SocketName: socket}
	replacementID, err := s.WindowID(staleIndex)
	if err != nil {
		t.Fatalf("WindowID (replacement): %v", err)
	}
	if replacementID == staleID {
		t.Fatalf("setup did not actually replace the window at index %d", staleIndex)
	}

	// Same index, same name, different window: must refuse.
	if err := s.KillWindow(staleID, "shell"); !errors.Is(err, ErrWindowChanged) {
		t.Fatalf("KillWindow with a closed window id = %v, want ErrWindowChanged", err)
	}
	after := windowsByID(t, socket, target)
	if len(after) != 2 {
		t.Fatalf("window count after refused kill = %d, want 2: %v", len(after), after)
	}
	if _, alive := after[replacementID]; !alive {
		t.Fatalf("replacement window %s died on a refused kill: %v", replacementID, after)
	}
}

// TestSession_KillWindow_RefusesRenamedWindow verifies the name guard: the
// window the user selected is still there (same id) but no longer what they
// read on screen, so KillWindow refuses rather than kill it under a name the
// user never confirmed.
func TestSession_KillWindow_RefusesRenamedWindow(t *testing.T) {
	requireTmux(t)
	socket, target := makeIsolatedServer(t)
	id := newNamedWindow(t, socket, target, "before")

	if out, err := exec.Command("tmux", "-L", socket, "rename-window", "-t", target+":"+id, "after").CombinedOutput(); err != nil {
		t.Fatalf("rename-window (setup): %v: %s", err, out)
	}

	s := &Session{Name: target, SocketName: socket}
	if err := s.KillWindow(id, "before"); !errors.Is(err, ErrWindowChanged) {
		t.Fatalf("KillWindow on a renamed window = %v, want ErrWindowChanged", err)
	}
	after := windowsByID(t, socket, target)
	if got, ok := after[id]; !ok || got.Name != "after" {
		t.Fatalf("renamed window must survive a refused kill untouched, got %+v", after)
	}
	// With the current name it is the same window the user now sees: kill.
	if err := s.KillWindow(id, "after"); err != nil {
		t.Fatalf("KillWindow with the live name: %v", err)
	}
	if _, alive := windowsByID(t, socket, target)[id]; alive {
		t.Fatalf("window %s still present after kill", id)
	}
}

// TestSession_Window verifies Window resolves the live index/name by id and
// reports ErrWindowChanged for an id the session does not have.
func TestSession_Window(t *testing.T) {
	requireTmux(t)
	socket, target := makeIsolatedServer(t)
	id := newNamedWindow(t, socket, target, "named")
	s := &Session{Name: target, SocketName: socket}

	live, err := s.Window(id)
	if err != nil {
		t.Fatalf("Window(%s): %v", id, err)
	}
	if live.ID != id || live.Name != "named" || live.Index != windowsByID(t, socket, target)[id].Index {
		t.Fatalf("Window(%s) = %+v, want id/name/index of the live window", id, live)
	}
	if _, err := s.Window("@999999"); !errors.Is(err, ErrWindowChanged) {
		t.Fatalf("Window on an unknown id = %v, want ErrWindowChanged", err)
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
		"sess": {{Index: 1, ID: "@1", Name: "agent"}, {Index: 2, ID: "@2", Name: "shell"}},
	}
	windowCacheTime = time.Now()
	windowCacheMu.Unlock()
	t.Cleanup(func() {
		windowCacheMu.Lock()
		windowCacheData = nil
		windowCacheMu.Unlock()
	})

	RemoveCachedWindow("sess", "@2")

	wins := GetCachedWindows("sess")
	if len(wins) != 1 {
		t.Fatalf("cached window count = %d, want 1 (windows: %v)", len(wins), wins)
	}
	if wins[0].ID != "@1" {
		t.Errorf("remaining window id = %s, want @1", wins[0].ID)
	}
}
