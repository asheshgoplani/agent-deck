package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// PR #474 ("show current task description inline for selected session") wires
// Claude's tmux pane_title through tmux.GetCachedPaneInfo into
// sessionRenderState.paneTitle, which renderSessionItem appends after the
// badges. The user-visible bug is that the inline title only updates the
// FIRST time — subsequent task transitions (Claude /rename or new task
// description) do not propagate to the rendered row.
//
// These tests pin the contract at the snapshot rebuild seam: regardless of
// how often or how stale the underlying tmux pane cache becomes,
// refreshSessionRenderSnapshot must reflect the current best-known pane title
// for the session. Bug repros target two failure modes:
//
//  1. Subsequent fresh cache values overwrite previous ones (the obvious read
//     test — sanity check that the rebuild loop reads the cache at all).
//  2. When the pane cache goes stale (background tick suppressed by navigation
//     hot-window or a slow list-panes), the rebuild must NOT blow away the
//     previously-known paneTitle. Today's behaviour clears it to "" because
//     GetCachedPaneInfo returns ok=false past the 4-second freshness threshold;
//     the renderer then drops the inline suffix until the next successful
//     RefreshPaneInfoCache, which the user perceives as "title only updated
//     once."

// instWithTmuxName returns an Instance whose GetTmuxSession() is non-nil and
// whose tmux Session.Name matches the provided cache key. Tests seed the cache
// under tmuxName; refreshSessionRenderSnapshot looks up cache entries by
// tmuxSess.Name.
func instWithTmuxName(t *testing.T, instID, tmuxName string) *session.Instance {
	t.Helper()
	inst := &session.Instance{ID: instID, Title: instID, Tool: "claude"}
	tmuxSess := tmux.ReconnectSessionLazy(tmuxName, instID, "/tmp", "claude", "idle")
	inst.SetTmuxSessionForTest(tmuxSess)
	return inst
}

// newHomeForSnapshotTest returns a Home with the atomic snapshot pre-seeded to
// an empty map (mirrors the production initialiser at home.go:792). Without
// this, atomic.Value.Load() returns nil and getSessionRenderSnapshot() falls
// through to the empty-map branch — which is fine for the very first call but
// hides the in-progress snapshot from later assertions.
func newHomeForSnapshotTest() *Home {
	h := &Home{}
	h.sessionRenderSnapshot.Store(make(map[string]sessionRenderState))
	return h
}

// TestRefreshSessionRenderSnapshot_PaneTitleUpdatesEachRefresh is the sanity
// check: when the tmux pane cache is fresh on every call,
// refreshSessionRenderSnapshot must propagate the latest title into the
// snapshot every time. Failure here would mean the rebuild loop is reading
// stale data even when the cache is fresh.
func TestRefreshSessionRenderSnapshot_PaneTitleUpdatesEachRefresh(t *testing.T) {
	const tmuxName = "agentdeck-snapshot-test-A"
	inst := instWithTmuxName(t, "inst-A", tmuxName)
	h := newHomeForSnapshotTest()

	// Tick 1: cache reports task one.
	tmux.SeedPaneInfoCacheForTest(t, map[string]tmux.PaneInfo{
		tmuxName: {Title: "⠐ implementing task one"},
	})
	h.refreshSessionRenderSnapshot([]*session.Instance{inst})
	if got := h.getSessionRenderSnapshot()[inst.ID].paneTitle; got != "implementing task one" {
		t.Fatalf("first refresh: paneTitle = %q, want %q", got, "implementing task one")
	}

	// Tick 2: cache reports task two — same instance, new title. The snapshot
	// must reflect the new value, not the cached "task one" from tick 1.
	tmux.SeedPaneInfoCacheForTest(t, map[string]tmux.PaneInfo{
		tmuxName: {Title: "⠐ implementing task two"},
	})
	h.refreshSessionRenderSnapshot([]*session.Instance{inst})
	if got := h.getSessionRenderSnapshot()[inst.ID].paneTitle; got != "implementing task two" {
		t.Errorf("second refresh: paneTitle = %q, want %q (REGRESSION: subsequent updates not propagating, PR #474)", got, "implementing task two")
	}
}

// TestRefreshSessionRenderSnapshot_PaneTitlePreservedWhenCacheStale is the
// regression pin for the "first time only" symptom. When the tmux cache goes
// stale (GetCachedPaneInfo returns ok=false past 4 s), the snapshot rebuild
// must keep the previously-known paneTitle so the rendered row continues to
// show the task description. Today's implementation clears it to "", which
// the user reads as "the title stopped updating."
//
// Why this matters: only backgroundStatusUpdate calls RefreshPaneInfoCache.
// processStatusUpdate (Bubble Tea ticker) calls refreshSessionRenderSnapshot
// without first refreshing the cache. If the background goroutine is
// suppressed (navigationHotUntil, dead tmux server, slow list-panes), the
// cache crosses the 4-second freshness boundary while processStatusUpdate
// keeps rebuilding the snapshot — and every rebuild zeroes paneTitle.
func TestRefreshSessionRenderSnapshot_PaneTitlePreservedWhenCacheStale(t *testing.T) {
	const tmuxName = "agentdeck-snapshot-test-B"
	inst := instWithTmuxName(t, "inst-B", tmuxName)
	h := newHomeForSnapshotTest()

	// Seed cache fresh, do a refresh, confirm the snapshot picked up the title.
	tmux.SeedPaneInfoCacheForTest(t, map[string]tmux.PaneInfo{
		tmuxName: {Title: "⠐ long-running task"},
	})
	h.refreshSessionRenderSnapshot([]*session.Instance{inst})
	if got := h.getSessionRenderSnapshot()[inst.ID].paneTitle; got != "long-running task" {
		t.Fatalf("setup refresh: paneTitle = %q, want %q", got, "long-running task")
	}

	// Cache goes stale (e.g. backgroundStatusUpdate suppressed for >4 s by
	// navigation hot-window). processStatusUpdate fires anyway and rebuilds
	// the snapshot — this is the call that today wipes paneTitle.
	tmux.ExpirePaneInfoCacheForTest(t)
	h.refreshSessionRenderSnapshot([]*session.Instance{inst})

	if got := h.getSessionRenderSnapshot()[inst.ID].paneTitle; got != "long-running task" {
		t.Errorf("stale-cache refresh: paneTitle = %q, want %q preserved (REGRESSION: rebuild zeroes title when cache is stale, PR #474)", got, "long-running task")
	}
}
