package statedb

import (
	"testing"
)

// loadGroupPaths is a small helper returning the set of persisted group paths.
func loadGroupPaths(t *testing.T, db *StateDB) map[string]bool {
	t.Helper()
	rows, err := db.LoadGroups()
	if err != nil {
		t.Fatalf("LoadGroups: %v", err)
	}
	set := make(map[string]bool, len(rows))
	for _, g := range rows {
		set[g.Path] = true
	}
	return set
}

// Regression for empty groups silently vanishing: a save with an INCOMPLETE
// group set (e.g. a stale or instances-only in-memory tree from another running
// instance) must not delete groups it doesn't know about. Replace-all semantics
// wiped them; populated groups self-healed from their sessions on reload but
// empty (session-less) groups were gone forever.
func TestSaveGroupsDoesNotDropUnknownGroups(t *testing.T) {
	db := newTestDB(t)

	// Initial authoritative save: a populated group and an EMPTY group.
	if err := db.SaveGroups([]*GroupRow{
		{Path: "alpha", Name: "alpha", Expanded: true, Order: 0},
		{Path: "empties", Name: "empties", Expanded: true, Order: 1},
	}); err != nil {
		t.Fatalf("initial SaveGroups: %v", err)
	}

	// A second, incomplete saver only knows about "alpha" (it never had
	// "empties" in its tree). This must NOT delete "empties".
	if err := db.SaveGroups([]*GroupRow{
		{Path: "alpha", Name: "alpha", Expanded: true, Order: 0},
	}); err != nil {
		t.Fatalf("incomplete SaveGroups: %v", err)
	}

	got := loadGroupPaths(t, db)
	if !got["empties"] {
		t.Fatalf("empty group was wiped by an incomplete save; have %v", got)
	}
	if !got["alpha"] {
		t.Fatalf("populated group missing after save; have %v", got)
	}
}

// SaveGroups must still update fields (rename, reorder, expand, default-path,
// max-concurrent) for groups it does know about — upsert, not insert-only.
func TestSaveGroupsUpdatesExistingFields(t *testing.T) {
	db := newTestDB(t)

	if err := db.SaveGroups([]*GroupRow{
		{Path: "g", Name: "old", Expanded: true, Order: 5, DefaultPath: "/a", MaxConcurrent: 1},
	}); err != nil {
		t.Fatalf("SaveGroups: %v", err)
	}
	if err := db.SaveGroups([]*GroupRow{
		{Path: "g", Name: "new", Expanded: false, Order: 2, DefaultPath: "/b", MaxConcurrent: 4},
	}); err != nil {
		t.Fatalf("SaveGroups update: %v", err)
	}

	rows, err := db.LoadGroups()
	if err != nil {
		t.Fatalf("LoadGroups: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 group, got %d", len(rows))
	}
	g := rows[0]
	if g.Name != "new" || g.Expanded != false || g.Order != 2 || g.DefaultPath != "/b" || g.MaxConcurrent != 4 {
		t.Fatalf("fields not upserted: %+v", g)
	}
}

// Intentional removal of a group (and its subgroups) must go through an explicit
// subtree delete, since SaveGroups no longer prunes.
func TestDeleteGroupSubtreeRemovesGroupAndDescendants(t *testing.T) {
	db := newTestDB(t)

	if err := db.SaveGroups([]*GroupRow{
		{Path: "parent", Name: "parent", Order: 0},
		{Path: "parent/child", Name: "child", Order: 1},
		{Path: "parent/child/grand", Name: "grand", Order: 2},
		{Path: "parental", Name: "parental", Order: 3}, // prefix look-alike, must survive
		{Path: "other", Name: "other", Order: 4},
	}); err != nil {
		t.Fatalf("SaveGroups: %v", err)
	}

	if err := db.DeleteGroupSubtree("parent"); err != nil {
		t.Fatalf("DeleteGroupSubtree: %v", err)
	}

	got := loadGroupPaths(t, db)
	for _, gone := range []string{"parent", "parent/child", "parent/child/grand"} {
		if got[gone] {
			t.Fatalf("%q should have been deleted; have %v", gone, got)
		}
	}
	for _, kept := range []string{"parental", "other"} {
		if !got[kept] {
			t.Fatalf("%q should have survived subtree delete; have %v", kept, got)
		}
	}
}
