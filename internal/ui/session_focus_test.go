package ui

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// buildFocusHome returns a Home with two groups (alpha: a1,a2; beta: b1,b2) and
// the instances slice so tests can read generated IDs.
func buildFocusHome(t *testing.T) (*Home, []*session.Instance) {
	t.Helper()

	home := NewHome()
	home.width = 120
	home.height = 40
	home.initialLoading = false

	instances := []*session.Instance{
		session.NewInstanceWithTool("a1", "/tmp/a1", "claude"),
		session.NewInstanceWithTool("a2", "/tmp/a2", "claude"),
		session.NewInstanceWithTool("b1", "/tmp/b1", "claude"),
		session.NewInstanceWithTool("b2", "/tmp/b2", "claude"),
	}
	instances[0].GroupPath = "alpha"
	instances[1].GroupPath = "alpha"
	instances[2].GroupPath = "beta"
	instances[3].GroupPath = "beta"

	home.instancesMu.Lock()
	home.instances = instances
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(instances)
	home.rebuildFlatItems()
	return home, instances
}

func TestSelectSessionByID_Visible(t *testing.T) {
	home, inst := buildFocusHome(t)
	want := home.flatItemIndexByID(inst[2].ID) // b1, currently visible
	if want < 0 {
		t.Fatal("precondition: b1 should be visible")
	}
	if !home.SelectSessionByID(inst[2].ID) {
		t.Fatal("SelectSessionByID returned false for visible session")
	}
	if home.cursor != want {
		t.Fatalf("cursor = %d, want %d", home.cursor, want)
	}
}

func TestSelectSessionByID_CollapsedGroup(t *testing.T) {
	home, inst := buildFocusHome(t)
	home.groupTree.CollapseGroup("beta")
	home.rebuildFlatItems()
	if home.flatItemIndexByID(inst[3].ID) >= 0 {
		t.Fatal("precondition: b2 should be hidden in collapsed group")
	}

	if !home.SelectSessionByID(inst[3].ID) {
		t.Fatal("SelectSessionByID returned false for collapsed-group session")
	}
	idx := home.flatItemIndexByID(inst[3].ID)
	if idx < 0 || home.cursor != idx {
		t.Fatalf("after select: cursor=%d, idx=%d (want equal, >=0)", home.cursor, idx)
	}
}

func TestSelectSessionByID_HiddenByStatusFilter(t *testing.T) {
	home, inst := buildFocusHome(t)
	// Give the target a status the filter excludes, and a sibling the filter
	// keeps (so the filter is not auto-cleared for matching nothing).
	inst[0].Status = session.StatusIdle    // a1 — target
	inst[1].Status = session.StatusRunning // a2 — keeps the filter alive
	home.statusFilter = session.StatusRunning
	home.rebuildFlatItems()
	if home.flatItemIndexByID(inst[0].ID) >= 0 {
		t.Fatal("precondition: a1 should be hidden by the running filter")
	}

	if !home.SelectSessionByID(inst[0].ID) {
		t.Fatal("SelectSessionByID returned false for filter-hidden session")
	}
	if home.statusFilter != "" {
		t.Fatalf("statusFilter = %q, want cleared", home.statusFilter)
	}
	idx := home.flatItemIndexByID(inst[0].ID)
	if idx < 0 || home.cursor != idx {
		t.Fatalf("after select: cursor=%d, idx=%d (want equal, >=0)", home.cursor, idx)
	}
}

func TestSelectSessionByID_UnknownID(t *testing.T) {
	home, _ := buildFocusHome(t)
	before := home.cursor
	if home.SelectSessionByID("no-such-id") {
		t.Fatal("SelectSessionByID returned true for unknown id")
	}
	if home.cursor != before {
		t.Fatalf("cursor moved on unknown id: %d -> %d", before, home.cursor)
	}
}

func TestSelectSessionByID_Archived(t *testing.T) {
	home, inst := buildFocusHome(t)
	inst[2].ArchivedAt = time.Now() // b1 archived
	home.rebuildFlatItems()
	before := home.cursor
	if home.SelectSessionByID(inst[2].ID) {
		t.Fatal("SelectSessionByID returned true for archived session")
	}
	if home.cursor != before {
		t.Fatalf("cursor moved selecting archived session: %d -> %d", before, home.cursor)
	}
}
