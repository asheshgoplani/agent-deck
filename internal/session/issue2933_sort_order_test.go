package session

import (
	"testing"
)

// Issue #2933: AddSession must place a new session after the highest Order in
// its group, not at len(group.Sessions), which lands mid-group once the
// group's orders have gaps or duplicates.
func TestIssue2933_AddSessionUsesMaxOrderPlusOne(t *testing.T) {
	SetGroupSortMode("creation")
	t.Cleanup(func() { SetGroupSortMode("creation") })

	a := &Instance{ID: "a", Title: "a", GroupPath: "g", Order: 0}
	b := &Instance{ID: "b", Title: "b", GroupPath: "g", Order: 5}
	c := &Instance{ID: "c", Title: "c", GroupPath: "g", Order: 5}
	tree := &GroupTree{
		groupDeletes: &groupDeletionQueue{},
		Groups: map[string]*Group{
			"g": {Name: "g", Path: "g", Expanded: true, Sessions: []*Instance{a, b, c}},
		},
		Expanded: map[string]bool{"g": true},
	}
	tree.rebuildGroupList()

	n := &Instance{ID: "n", Title: "n", GroupPath: "g"}
	tree.AddSession(n)
	if n.Order != 6 {
		t.Fatalf("AddSession on orders {0,5,5}: got Order %d, want 6", n.Order)
	}
}

// Issue #2933: a group without duplicates keeps its stored orders untouched
// (gaps are left alone; only ties are ambiguous).
func TestIssue2933_LoadKeepsUniqueOrders(t *testing.T) {
	SetGroupSortMode("creation")
	insts := []*Instance{
		{ID: "a", Title: "a", GroupPath: "g", Order: 0},
		{ID: "b", Title: "b", GroupPath: "g", Order: 4},
		{ID: "c", Title: "c", GroupPath: "g", Order: 9},
	}
	NewGroupTree(insts)
	if insts[0].Order != 0 || insts[1].Order != 4 || insts[2].Order != 9 {
		t.Fatalf("unique orders changed: %d %d %d", insts[0].Order, insts[1].Order, insts[2].Order)
	}
}
