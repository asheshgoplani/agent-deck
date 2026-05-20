// Issue #1066 — Header session counter shows 0 even when remote sessions
// are running. countSessionStatuses must include remote sessions in its
// totals so the header counter and the filter-bar pill counts match what
// the user sees in the session list.
//
// Reporter: @ddorman-dn. The TUI consumer was iterating only
// h.sessionRenderSnapshot (local instances) and never looking at
// h.remoteSessions, so any session reachable only via SSH was invisible
// to status aggregation.

package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestIssue1066_RemoteSessions_CountedInStatusCounter(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	// No local instances. countSessionStatuses must still pick up remotes.
	home.refreshSessionRenderSnapshot(nil)

	// Two remotes, one with a running claude + a waiting claude, one with
	// an idle claude. Counter must aggregate across remotes.
	home.remoteSessionsMu.Lock()
	home.remoteSessions = map[string][]session.RemoteSessionInfo{
		"dev": {
			{ID: "r1", Title: "running-claude", Tool: "claude", Status: "running", RemoteName: "dev"},
			{ID: "r2", Title: "waiting-claude", Tool: "claude", Status: "waiting", RemoteName: "dev"},
		},
		"xai": {
			{ID: "r3", Title: "idle-claude", Tool: "claude", Status: "idle", RemoteName: "xai"},
		},
	}
	home.remoteSessionsMu.Unlock()

	// Invalidate the 500ms cache so the next call recomputes.
	home.cachedStatusCounts.valid.Store(false)

	running, waiting, idle, stopped, errored := home.countSessionStatuses()
	_ = stopped

	if running != 1 {
		t.Errorf("running = %d, want 1 (remote dev/r1) — counter is ignoring remote sessions", running)
	}
	if waiting != 1 {
		t.Errorf("waiting = %d, want 1 (remote dev/r2) — counter is ignoring remote sessions", waiting)
	}
	if idle != 1 {
		t.Errorf("idle = %d, want 1 (remote xai/r3) — counter is ignoring remote sessions", idle)
	}
	if errored != 0 {
		t.Errorf("errored = %d, want 0", errored)
	}
}

// TestIssue1066_RemoteSession_ToolFieldPreservedThroughItem asserts the
// Tool field survives the path from RemoteSessionInfo → session.Item →
// renderer. If this regresses, every downstream consumer falls back to
// "generic" rendering — the original reporter's screenshot pinned this.
func TestIssue1066_RemoteSession_ToolFieldPreservedThroughItem(t *testing.T) {
	remote := session.RemoteSessionInfo{
		ID:         "r1",
		Title:      "demo",
		Tool:       "claude",
		Status:     "running",
		RemoteName: "dev",
	}
	item := session.Item{
		Type:          session.ItemTypeRemoteSession,
		RemoteSession: &remote,
		RemoteName:    "dev",
	}

	if item.RemoteSession == nil {
		t.Fatal("RemoteSession pointer must not be nil after Item construction")
	}
	if item.RemoteSession.Tool != "claude" {
		t.Fatalf("item.RemoteSession.Tool = %q, want claude — renderer reads this field for tool-specific framing", item.RemoteSession.Tool)
	}
}
