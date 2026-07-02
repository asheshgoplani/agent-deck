package session

import (
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

func emptyGroupTestStorage(t *testing.T) *Storage {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := statedb.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Storage{db: db, dbPath: dbPath, profile: "_test"}
}

func loadGroupPathSet(t *testing.T, s *Storage) map[string]bool {
	t.Helper()
	_, groups, err := s.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	set := make(map[string]bool, len(groups))
	for _, g := range groups {
		set[g.Path] = true
	}
	return set
}

// Regression for empty (session-less) groups silently disappearing.
//
// Real-world trigger: a second running instance (allow_multiple) or the
// instances-only NewGroupTree fallback persists a tree that never contained an
// empty group. With the old replace-all SaveGroups this deleted the empty group
// for good (populated groups self-heal from their sessions; empty ones cannot).
// With additive SaveGroups, a partial save must leave unknown groups intact.
func TestSaveWithGroupsPreservesEmptyGroupAcrossStaleSave(t *testing.T) {
	s := emptyGroupTestStorage(t)

	inst := NewInstanceWithTool("a1", "/tmp/a1", "claude")
	inst.GroupPath = "alpha"
	instances := []*Instance{inst}

	// Authoritative save: a populated group "alpha" and an EMPTY group "empties".
	full := NewGroupTreeWithGroups(instances, []*GroupData{
		{Name: "alpha", Path: "alpha", Expanded: true},
		{Name: "empties", Path: "empties", Expanded: true},
	})
	if err := s.SaveWithGroups(instances, full); err != nil {
		t.Fatalf("authoritative SaveWithGroups: %v", err)
	}
	if got := loadGroupPathSet(t, s); !got["empties"] || !got["alpha"] {
		t.Fatalf("setup failed, groups on disk: %v", got)
	}

	// Stale/incomplete saver: an instances-only tree has "alpha" (from the
	// session) but no "empties". This must NOT delete "empties".
	stale := NewGroupTree(instances)
	if _, ok := stale.Groups["empties"]; ok {
		t.Fatalf("precondition: stale tree should not know about 'empties'")
	}
	if err := s.SaveWithGroups(instances, stale); err != nil {
		t.Fatalf("stale SaveWithGroups: %v", err)
	}

	got := loadGroupPathSet(t, s)
	if !got["empties"] {
		t.Fatalf("empty group was wiped by an instances-only save; groups on disk: %v", got)
	}
	if !got["alpha"] {
		t.Fatalf("populated group missing after stale save; groups on disk: %v", got)
	}
}

// The explicit subtree delete (used by intentional delete/rename/move) removes
// the group and its descendants while leaving prefix look-alikes intact.
func TestDeleteGroupSubtreeViaStorage(t *testing.T) {
	s := emptyGroupTestStorage(t)

	tree := NewGroupTreeWithGroups(nil, []*GroupData{
		{Name: "parent", Path: "parent"},
		{Name: "child", Path: "parent/child"},
		{Name: "parental", Path: "parental"},
	})
	if err := s.SaveWithGroups(nil, tree); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	if err := s.DeleteGroupSubtree("parent"); err != nil {
		t.Fatalf("DeleteGroupSubtree: %v", err)
	}

	got := loadGroupPathSet(t, s)
	if got["parent"] || got["parent/child"] {
		t.Fatalf("subtree not deleted: %v", got)
	}
	if !got["parental"] {
		t.Fatalf("prefix look-alike 'parental' should survive: %v", got)
	}
}
