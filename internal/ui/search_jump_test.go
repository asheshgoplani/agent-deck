package ui

// Regression tests for #2655: `/`-search Enter silently did nothing when the
// target session was hidden by an active status or time filter — the handler
// scanned the filtered flat list and never found the row, with no notice and
// no jump. jumpToSessionByID now clears the active filters the same way the
// empty-view fallbacks do, jumps, and restores them only when the session is
// genuinely absent.

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func makeSearchJumpHome() *Home {
	h := seamBNewHome()
	h.activeFilterExcludes = (session.DisplaySettings{}).GetActiveFilterExcludes()
	live := session.NewInstanceWithTool("live", "/tmp/live", "claude")
	live.ID = "live"
	live.GroupPath = "alpha"
	live.Status = session.StatusRunning
	dead := session.NewInstanceWithTool("dead", "/tmp/dead", "claude")
	dead.ID = "dead"
	dead.GroupPath = "alpha"
	dead.Status = session.StatusStopped
	h.groupTree = session.NewGroupTree([]*session.Instance{live, dead})
	h.rebuildFlatItems()
	return h
}

func cursorSessionID(h *Home) string {
	if h.cursor >= 0 && h.cursor < len(h.flatItems) {
		item := h.flatItems[h.cursor]
		if item.Type == session.ItemTypeSession && item.Session != nil {
			return item.Session.ID
		}
	}
	return ""
}

func TestSearchJumpClearsStatusFilterForFilteredTarget(t *testing.T) {
	h := makeSearchJumpHome()
	h.statusFilter = FilterModeActive // hides the stopped "dead" session
	h.rebuildFlatItems()
	// Precondition: the target really is filtered out.
	for _, item := range h.flatItems {
		if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.ID == "dead" {
			t.Fatal("test setup: dead session should be filtered out under FilterModeActive")
		}
	}

	if !h.jumpToSessionByID("dead", "alpha") {
		t.Fatal("jump should succeed after clearing the filter")
	}
	if got := cursorSessionID(h); got != "dead" {
		t.Fatalf("cursor landed on %q, want dead", got)
	}
	if h.statusFilter != "" {
		t.Fatalf("status filter should be cleared by the jump fallback, got %q", h.statusFilter)
	}
}

func TestSearchJumpKeepsFiltersWhenTargetVisible(t *testing.T) {
	h := makeSearchJumpHome()
	h.statusFilter = FilterModeActive
	h.rebuildFlatItems()

	if !h.jumpToSessionByID("live", "alpha") {
		t.Fatal("jump to a visible session should succeed without touching filters")
	}
	if got := cursorSessionID(h); got != "live" {
		t.Fatalf("cursor landed on %q, want live", got)
	}
	if h.statusFilter != FilterModeActive {
		t.Fatalf("filter should be untouched when the target was visible, got %q", h.statusFilter)
	}
}

func TestSearchJumpRestoresFiltersWhenTargetGone(t *testing.T) {
	h := makeSearchJumpHome()
	// FilterModeActive keeps a match (live), so the house fallback never
	// clears it — making the restore assertion deterministic.
	h.statusFilter = FilterModeActive
	h.rebuildFlatItems()

	if h.jumpToSessionByID("nonexistent", "alpha") {
		t.Fatal("jump to a missing session must fail")
	}
	if h.statusFilter != FilterModeActive {
		t.Fatalf("filters should be restored when the session is genuinely absent, got %q", h.statusFilter)
	}
}

func TestSearchJumpExpandsCollapsedGroup(t *testing.T) {
	h := makeSearchJumpHome()
	h.groupTree.CollapseGroup("alpha")
	h.rebuildFlatItems()
	// Precondition: collapsed group hides both sessions.
	for _, item := range h.flatItems {
		if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.ID == "live" {
			t.Fatal("test setup: collapsed group should hide live")
		}
	}

	if !h.jumpToSessionByID("live", "alpha") {
		t.Fatal("jump should expand the group and find the session")
	}
	if got := cursorSessionID(h); got != "live" {
		t.Fatalf("cursor landed on %q, want live", got)
	}
}

// CodeRabbit (PR #2656): a failed jump must restore the target group's fold
// state too — expanding it for the scan must not leak when the session turns
// out to be missing.
func TestSearchJumpRestoresGroupFoldsOnMiss(t *testing.T) {
	h := makeSearchJumpHome()
	h.groupTree.CollapseGroup("alpha")
	h.rebuildFlatItems()
	h.statusFilter = FilterModeActive
	h.rebuildFlatItems()

	if h.jumpToSessionByID("nonexistent", "alpha") {
		t.Fatal("jump to a missing session must fail")
	}
	for _, g := range h.groupTree.GroupList {
		if g.Path == "alpha" && g.Expanded {
			t.Fatal("failed jump must re-collapse the target group")
		}
	}
	if h.statusFilter != FilterModeActive {
		t.Fatalf("filters should also be restored, got %q", h.statusFilter)
	}
}

func TestSearchJumpRestoresFoldsWithoutFilters(t *testing.T) {
	h := makeSearchJumpHome()
	h.groupTree.CollapseGroup("alpha")
	h.rebuildFlatItems()

	if h.jumpToSessionByID("nonexistent", "alpha") {
		t.Fatal("jump to a missing session must fail")
	}
	for _, g := range h.groupTree.GroupList {
		if g.Path == "alpha" && g.Expanded {
			t.Fatal("failed jump must re-collapse the target group")
		}
	}
}
