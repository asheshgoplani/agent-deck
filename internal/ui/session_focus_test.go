package ui

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
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

func newUITempStateDB(t *testing.T) *statedb.StateDB {
	t.Helper()
	db, err := statedb.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
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

func TestConsumeFocusRequest_Fresh(t *testing.T) {
	home, inst := buildFocusHome(t)
	db := newUITempStateDB(t)

	// Collapse beta so the target needs revealing — proves consume drives reveal.
	home.groupTree.CollapseGroup("beta")
	home.rebuildFlatItems()

	if err := session.WriteFocusRequest(db, inst[3].ID, time.Now().UnixNano()); err != nil {
		t.Fatalf("seed request: %v", err)
	}

	home.consumeFocusRequest(db)

	idx := home.flatItemIndexByID(inst[3].ID)
	if idx < 0 || home.cursor != idx {
		t.Fatalf("fresh request not selected: cursor=%d idx=%d", home.cursor, idx)
	}
	if raw, _ := session.ReadFocusRequest(db); raw != "" {
		t.Fatalf("row not cleared after consume: %q", raw)
	}
}

func TestConsumeFocusRequest_Stale(t *testing.T) {
	home, inst := buildFocusHome(t)
	db := newUITempStateDB(t)
	before := home.cursor

	staleTS := time.Now().Add(-time.Hour).UnixNano()
	if err := session.WriteFocusRequest(db, inst[3].ID, staleTS); err != nil {
		t.Fatalf("seed request: %v", err)
	}

	home.consumeFocusRequest(db)

	if home.cursor != before {
		t.Fatalf("stale request moved cursor: %d -> %d", before, home.cursor)
	}
	if raw, _ := session.ReadFocusRequest(db); raw != "" {
		t.Fatalf("stale row not cleared: %q", raw)
	}
}

func TestConsumeFocusRequest_Empty(t *testing.T) {
	home, _ := buildFocusHome(t)
	db := newUITempStateDB(t)
	before := home.cursor

	home.consumeFocusRequest(db) // no row present

	if home.cursor != before {
		t.Fatalf("empty request moved cursor: %d -> %d", before, home.cursor)
	}
}

func TestConsumeFocusRequest_NilDB(t *testing.T) {
	home, _ := buildFocusHome(t)
	home.consumeFocusRequest(nil) // must not panic
}
