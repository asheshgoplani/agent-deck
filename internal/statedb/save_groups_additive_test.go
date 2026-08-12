package statedb

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDeleteInstanceAndSaveGroupsRollsBackRowOnGroupFailure(t *testing.T) {
	db := newTestDB(t)
	const id = "atomic-delete-group-failure"
	if err := db.SaveInstance(&InstanceRow{
		ID: id, Title: "keep", ProjectPath: "/tmp", GroupPath: "g",
		Tool: "shell", Status: "stopped", CreatedAt: time.Now(), ToolData: json.RawMessage("{}"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`CREATE TRIGGER force_group_failure
		BEFORE INSERT ON groups BEGIN SELECT RAISE(FAIL, 'forced group failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteInstanceAndSaveGroups(id, []*GroupRow{{Path: "g", Name: "g"}}); err == nil {
		t.Fatal("atomic lifecycle commit unexpectedly succeeded")
	}
	exists, err := db.InstanceExists(id)
	if err != nil || !exists {
		t.Fatalf("row deletion was not rolled back after group failure: exists=%v err=%v", exists, err)
	}
	if groups, err := db.LoadGroups(); err != nil || len(groups) != 0 {
		t.Fatalf("group write survived failed transaction: %#v, %v", groups, err)
	}
}

func TestDeleteInstanceAndSaveGroupsDeletesRowAndPersistsEveryGroupField(t *testing.T) {
	db := newTestDB(t)
	const id = "atomic-delete-group-success"
	if err := db.SaveInstance(&InstanceRow{
		ID: id, Title: "delete", ProjectPath: "/tmp", GroupPath: "team",
		Tool: "shell", Status: "stopped", CreatedAt: time.Now(), ToolData: json.RawMessage("{}"),
	}); err != nil {
		t.Fatal(err)
	}
	want := &GroupRow{
		Path: "team/nested", Name: "Nested Team", Expanded: true, Order: 7,
		DefaultPath: "/projects/team", MaxConcurrent: 4,
	}
	if err := db.DeleteInstanceAndSaveGroups(id, []*GroupRow{want}); err != nil {
		t.Fatal(err)
	}
	if exists, err := db.InstanceExists(id); err != nil || exists {
		t.Fatalf("row survived successful atomic delete: exists=%v err=%v", exists, err)
	}
	groups, err := db.LoadGroups()
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups after atomic commit = %#v, %v", groups, err)
	}
	got := groups[0]
	if got.Path != want.Path || got.Name != want.Name || got.Expanded != want.Expanded ||
		got.Order != want.Order || got.DefaultPath != want.DefaultPath || got.MaxConcurrent != want.MaxConcurrent {
		t.Fatalf("persisted group = %#v, want %#v", got, want)
	}
}

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
