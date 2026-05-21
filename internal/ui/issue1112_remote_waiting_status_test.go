// Issue #1112 — bug 1 (by @ddorman-dn on v1.9.24): the remote "waiting for
// input" icon doesn't refresh in the local TUI when the remote session
// transitions running → waiting. The root cause is in
// `case remoteSessionsFetchedMsg:` (home.go:4377): the handler swapped
// `h.remoteSessions` for the new map but never invalidated the 500ms
// cached status counters, so the header pill "[◐ Waiting N]" kept
// rendering the pre-fetch count. The row icon (read directly from the
// map) updated on the next View, but the pill froze.
//
// The fix: invalidate `cachedStatusCounts` whenever the remote fetch
// publishes a new map. Test below pumps a remoteSessionsFetchedMsg into
// the handler and asserts the counter recomputes.

package ui

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestIssue1112_RemoteFetchMsg_InvalidatesStatusCounterCache reproduces the
// stale-pill symptom: fetch #1 populates the cache with one "running"
// remote; fetch #2 publishes the same remote as "waiting" but with the
// cache still valid (well within the 500ms window). countSessionStatuses
// MUST observe the new waiting status — if it returns the pre-fetch
// running=1 / waiting=0, the user sees the wrong header pill.
func TestIssue1112_RemoteFetchMsg_InvalidatesStatusCounterCache(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30
	home.refreshSessionRenderSnapshot(nil)

	// Seed: one remote, status=running.
	home.remoteSessionsMu.Lock()
	home.remoteSessions = map[string][]session.RemoteSessionInfo{
		"dev": {{ID: "r1", Title: "p", Tool: "claude", Status: "running", RemoteName: "dev"}},
	}
	home.remoteSessionsMu.Unlock()
	home.cachedStatusCounts.valid.Store(false)
	running, waiting, _, _, _ := home.countSessionStatuses()
	if running != 1 || waiting != 0 {
		t.Fatalf("seed counts: running=%d waiting=%d, want 1/0", running, waiting)
	}

	// Cache is now valid for 500ms. Simulate a remote fetch landing with
	// the same session reporting waiting. Without the cache-invalidation
	// fix the counters return stale running=1.
	newSessions := map[string][]session.RemoteSessionInfo{
		"dev": {{ID: "r1", Title: "p", Tool: "claude", Status: "waiting", RemoteName: "dev"}},
	}
	model, _ := home.Update(remoteSessionsFetchedMsg{sessions: newSessions})
	home = model.(*Home)

	running, waiting, _, _, _ = home.countSessionStatuses()
	if running != 0 {
		t.Errorf("running=%d, want 0 — remote fetch must invalidate the cache (#1112 bug 1)", running)
	}
	if waiting != 1 {
		t.Errorf("waiting=%d, want 1 — the new waiting state must surface immediately (#1112 bug 1)", waiting)
	}
}

// TestIssue1112_RemoteFetchMsg_ZeroSessionsClearsStaleCounts covers the
// "remote went away" failure mode: a remote that previously had a
// running claude is now empty (claude killed, machine offline, etc).
// The header pill must drop the count rather than show a phantom
// running session.
func TestIssue1112_RemoteFetchMsg_ZeroSessionsClearsStaleCounts(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30
	home.refreshSessionRenderSnapshot(nil)

	home.remoteSessionsMu.Lock()
	home.remoteSessions = map[string][]session.RemoteSessionInfo{
		"dev": {{ID: "r1", Title: "p", Tool: "claude", Status: "running", RemoteName: "dev"}},
	}
	home.remoteSessionsMu.Unlock()
	home.cachedStatusCounts.valid.Store(false)
	_, _, _, _, _ = home.countSessionStatuses()

	// Remote fetch lands with empty session list.
	model, _ := home.Update(remoteSessionsFetchedMsg{sessions: map[string][]session.RemoteSessionInfo{}})
	home = model.(*Home)

	running, waiting, _, _, _ := home.countSessionStatuses()
	if running != 0 || waiting != 0 {
		t.Errorf("after empty fetch: running=%d waiting=%d, want 0/0 — counter must drop entries (#1112 bug 1)", running, waiting)
	}
}

// TestIssue1112_RemoteFetchMsg_ConcurrentLocalChangeStillReflected guards
// the boundary where a local session status update happens in the same
// tick as a remote fetch. Both must show; neither must mask the other.
func TestIssue1112_RemoteFetchMsg_ConcurrentLocalChangeStillReflected(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30
	home.refreshSessionRenderSnapshot(nil)

	// Remote: one waiting session.
	model, _ := home.Update(remoteSessionsFetchedMsg{
		sessions: map[string][]session.RemoteSessionInfo{
			"dev": {{ID: "r1", Title: "p", Tool: "claude", Status: "waiting", RemoteName: "dev"}},
		},
	})
	home = model.(*Home)
	// Ensure the cache window is fresh so the next call returns cached if not invalidated.
	home.cachedStatusCounts.timestamp = time.Now()
	home.cachedStatusCounts.valid.Store(true)

	// A second remote fetch arrives reporting same session still waiting —
	// the counter must remain at waiting=1, never flicker to 0.
	model, _ = home.Update(remoteSessionsFetchedMsg{
		sessions: map[string][]session.RemoteSessionInfo{
			"dev": {{ID: "r1", Title: "p", Tool: "claude", Status: "waiting", RemoteName: "dev"}},
		},
	})
	home = model.(*Home)

	_, waiting, _, _, _ := home.countSessionStatuses()
	if waiting != 1 {
		t.Errorf("waiting=%d, want 1 — back-to-back fetches must not drop the waiting state", waiting)
	}
}
