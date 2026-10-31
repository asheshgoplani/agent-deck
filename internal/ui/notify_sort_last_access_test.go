package ui

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// #2949: with [notifications] sort = "last_accessed", jumping with the bar's
// own Ctrl+b 1-9 keys (tmux switch-client plus the ack signal file) must count
// as a visit, and detaching must stamp the session on screen, not the one the
// TUI first attached to.

func notifSortHome(t *testing.T) (*Home, *session.NotificationManager, map[string]*session.Instance) {
	t.Helper()
	_ = tmux.ReadAndClearAckSignal()
	t.Cleanup(func() { _ = tmux.ReadAndClearAckSignal() })

	now := time.Now()
	h := NewHome()
	byID := map[string]*session.Instance{}
	// "a" was attached from the TUI most recently; "b" and "c" earlier.
	for id, accessed := range map[string]time.Time{
		"a": now.Add(-1 * time.Minute),
		"b": now.Add(-3 * time.Minute),
		"c": now.Add(-2 * time.Minute),
	} {
		inst := &session.Instance{ID: id, Title: id, Status: session.StatusWaiting, CreatedAt: now.Add(-time.Hour), LastAccessedAt: accessed}
		byID[id] = inst
	}
	h.instancesMu.Lock()
	h.instances = []*session.Instance{byID["a"], byID["b"], byID["c"]}
	for id, inst := range byID {
		h.instanceByID[id] = inst
	}
	h.instancesMu.Unlock()

	nm := session.NewNotificationManager(6, true, false)
	nm.SetSortMode(session.NotificationSortLastAccessed)
	return h, nm, byID
}

func notifSortOrder(nm *session.NotificationManager) []string {
	var ids []string
	for _, e := range nm.GetEntries() {
		ids = append(ids, e.SessionID)
	}
	return ids
}

func assertNotifSortOrder(t *testing.T, nm *session.NotificationManager, want ...string) {
	t.Helper()
	got := notifSortOrder(nm)
	if len(got) != len(want) {
		t.Fatalf("bar order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("bar order = %v, want %v", got, want)
		}
	}
}

// jump simulates Ctrl+b N to id: the binding writes the ack signal, the
// background sync consumes it and syncs the bar with id as current.
func notifSortJump(t *testing.T, h *Home, nm *session.NotificationManager, id string) {
	t.Helper()
	if err := tmux.WriteAckSignal(id); err != nil {
		t.Fatalf("WriteAckSignal: %v", err)
	}
	if got := h.consumeAckSignal(); got != id {
		t.Fatalf("consumeAckSignal() = %q, want %q", got, id)
	}
	h.instancesMu.RLock()
	insts := append([]*session.Instance(nil), h.instances...)
	h.instancesMu.RUnlock()
	nm.SyncFromInstances(insts, id)
}

func TestNotifySortLastAccess_CtrlBJumpMarksAccessed(t *testing.T) {
	h, nm, byID := notifSortHome(t)
	before := byID["b"].GetLastAccessedAtThreadSafe()

	// Attached to a, jump to b, then to c.
	notifSortJump(t, h, nm, "b")
	if !byID["b"].GetLastAccessedAtThreadSafe().After(before) {
		t.Fatalf("jump to b did not stamp its last access")
	}
	notifSortJump(t, h, nm, "c")

	// On c now: b was used just before, so it is [1], ahead of a.
	assertNotifSortOrder(t, nm, "b", "a")
}

func TestNotifySortLastAccess_DetachStampsSessionOnScreen(t *testing.T) {
	h, nm, byID := notifSortHome(t)
	h.isAttaching.Store(true)
	t.Cleanup(func() { h.isAttaching.Store(false) })

	// TUI attached to a; Ctrl+b to b, then to c; detach.
	notifSortJump(t, h, nm, "b")
	notifSortJump(t, h, nm, "c")
	aBefore := byID["a"].GetLastAccessedAtThreadSafe()
	h.markDetachAccessed(byID["a"])

	if got := byID["a"].GetLastAccessedAtThreadSafe(); !got.Equal(aBefore) {
		t.Fatalf("detach stamped a (first attached) instead of c (on screen)")
	}
	h.lastNotifSwitchMu.Lock()
	switched := h.lastNotifSwitchID
	h.lastNotifSwitchMu.Unlock()
	if switched != "c" {
		t.Fatalf("lastNotifSwitchID = %q after detach stamp, want %q (cursor sync still needs it)", switched, "c")
	}

	h.instancesMu.RLock()
	insts := append([]*session.Instance(nil), h.instances...)
	h.instancesMu.RUnlock()
	nm.SyncFromInstances(insts, "")
	assertNotifSortOrder(t, nm, "c", "b", "a")
}

// Without a notification switch, detach stamps the attached session as before.
func TestNotifySortLastAccess_DetachWithoutSwitchStampsAttached(t *testing.T) {
	h, _, byID := notifSortHome(t)
	before := byID["b"].GetLastAccessedAtThreadSafe()
	h.markDetachAccessed(byID["b"])
	if !byID["b"].GetLastAccessedAtThreadSafe().After(before) {
		t.Fatalf("detach without a switch did not stamp the attached session")
	}
}

// The background stamp races the UI goroutine's readers (session switcher
// order, group sort) unless both sides take the instance lock. Run under -race.
func TestNotifySortLastAccess_BackgroundStampRaceWithUIReaders(t *testing.T) {
	h, nm, byID := notifSortHome(t)
	// After NewHome, which applies the configured group sort mode.
	session.SetGroupSortMode("actionable")
	t.Cleanup(func() { session.SetGroupSortMode("creation") })
	insts := []*session.Instance{byID["a"], byID["b"], byID["c"]}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			id := insts[i%len(insts)].ID
			_ = tmux.WriteAckSignal(id)
			h.consumeAckSignal()
			nm.SyncFromInstances(insts, id)
		}
	}()
	// Keep reading for as long as the background side is stamping. Only the
	// group sort reads here: mixing in readers that lock would add
	// happens-before edges and hide a missing lock from the race detector.
	for {
		sorted := append([]*session.Instance(nil), insts...)
		session.SortInstancesByActionable(sorted)
		select {
		case <-done:
			return
		default:
		}
	}
}
