package main

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// helper: create storage, add N root groups, return (storage, instances, groupTree).
// Each call overwrites the _test profile, so tests are independent when run sequentially.
func setupGroupsForReorder(t *testing.T, names ...string) *session.Storage {
	t.Helper()
	storage, err := session.NewStorageWithProfile("_test")
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}

	instances := []*session.Instance{}
	groupTree := session.NewGroupTreeWithGroups(instances, nil)

	for _, name := range names {
		groupTree.CreateGroup(name)
	}

	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	return storage
}

// helper: reload groups from storage and return ordered paths (excluding default group)
func reloadGroupPaths(t *testing.T, storage *session.Storage) []string {
	t.Helper()
	_, groups, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}

	instances := []*session.Instance{}
	tree := session.NewGroupTreeWithGroups(instances, groups)

	var paths []string
	for _, g := range tree.GroupList {
		if g.Path == session.DefaultGroupPath {
			continue
		}
		paths = append(paths, g.Path)
	}
	return paths
}

func TestGroupReorderUp(t *testing.T) {
	storage := setupGroupsForReorder(t, "Alpha", "Beta", "Gamma")

	// Move Beta up — should swap with Alpha
	handleGroupReorder("_test", []string{"Beta", "--up"})

	paths := reloadGroupPaths(t, storage)
	if len(paths) < 3 {
		t.Fatalf("expected 3 groups, got %d", len(paths))
	}
	if paths[0] != "Beta" || paths[1] != "Alpha" || paths[2] != "Gamma" {
		t.Errorf("expected [Beta Alpha Gamma], got %v", paths)
	}
}

func TestGroupReorderDown(t *testing.T) {
	storage := setupGroupsForReorder(t, "Alpha", "Beta", "Gamma")

	// Move Beta down — should swap with Gamma
	handleGroupReorder("_test", []string{"Beta", "--down"})

	paths := reloadGroupPaths(t, storage)
	if len(paths) < 3 {
		t.Fatalf("expected 3 groups, got %d", len(paths))
	}
	if paths[0] != "Alpha" || paths[1] != "Gamma" || paths[2] != "Beta" {
		t.Errorf("expected [Alpha Gamma Beta], got %v", paths)
	}
}

func TestGroupReorderPosition(t *testing.T) {
	storage := setupGroupsForReorder(t, "Alpha", "Beta", "Gamma")

	// Move Gamma to position 0
	handleGroupReorder("_test", []string{"Gamma", "--position", "0"})

	paths := reloadGroupPaths(t, storage)
	if len(paths) < 3 {
		t.Fatalf("expected 3 groups, got %d", len(paths))
	}
	if paths[0] != "Gamma" || paths[1] != "Alpha" || paths[2] != "Beta" {
		t.Errorf("expected [Gamma Alpha Beta], got %v", paths)
	}
}

func TestGroupReorderAlreadyAtTop(t *testing.T) {
	storage := setupGroupsForReorder(t, "Alpha", "Beta", "Gamma")

	// Move Alpha up — already first, should be no-op
	handleGroupReorder("_test", []string{"Alpha", "--up"})

	paths := reloadGroupPaths(t, storage)
	if len(paths) < 3 {
		t.Fatalf("expected 3 groups, got %d", len(paths))
	}
	if paths[0] != "Alpha" || paths[1] != "Beta" || paths[2] != "Gamma" {
		t.Errorf("expected [Alpha Beta Gamma], got %v", paths)
	}
}

func TestGroupReorderAlreadyAtBottom(t *testing.T) {
	storage := setupGroupsForReorder(t, "Alpha", "Beta", "Gamma")

	// Move Gamma down — already last, should be no-op
	handleGroupReorder("_test", []string{"Gamma", "--down"})

	paths := reloadGroupPaths(t, storage)
	if len(paths) < 3 {
		t.Fatalf("expected 3 groups, got %d", len(paths))
	}
	if paths[0] != "Alpha" || paths[1] != "Beta" || paths[2] != "Gamma" {
		t.Errorf("expected [Alpha Beta Gamma], got %v", paths)
	}
}

func TestGroupReorderPositionClamp(t *testing.T) {
	storage := setupGroupsForReorder(t, "Alpha", "Beta", "Gamma")

	// Move Alpha to position 99 (should clamp to last)
	handleGroupReorder("_test", []string{"Alpha", "--position", "99"})

	paths := reloadGroupPaths(t, storage)
	if len(paths) < 3 {
		t.Fatalf("expected 3 groups, got %d", len(paths))
	}
	if paths[0] != "Beta" || paths[1] != "Gamma" || paths[2] != "Alpha" {
		t.Errorf("expected [Beta Gamma Alpha], got %v", paths)
	}
}

// TestNormalizeGroupPathCasePreserving verifies that normalizeGroupPath does not
// lowercase its argument. GroupTree.Groups is keyed by the raw stored path, so
// lowercasing here would make any group with uppercase letters unreachable.
func TestNormalizeGroupPathCasePreserving(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"work", "work"},
		{"Work", "Work"},
		{"My Projects", "My-Projects"},
		{"work/Frontend", "work/Frontend"},
	}
	for _, tc := range cases {
		got := normalizeGroupPath(tc.input)
		if got != tc.want {
			t.Errorf("normalizeGroupPath(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// TestNormalizeGroupPathMatchesStoredKey verifies that after creating an uppercase
// group via GroupTree.CreateGroup, the result of normalizeGroupPath on the same
// name is a key that exists in GroupTree.Groups (regression guard for issue #1488).
func TestNormalizeGroupPathMatchesStoredKey(t *testing.T) {
	tree := session.NewGroupTreeWithGroups([]*session.Instance{}, nil)
	tree.CreateGroup("Parent")

	normalized := normalizeGroupPath("Parent")
	if _, exists := tree.Groups[normalized]; !exists {
		t.Errorf("normalizeGroupPath(%q) = %q, but Groups[%q] does not exist; stored keys: %v",
			"Parent", normalized, normalized, groupKeys(tree))
	}
}

// TestGroupDeleteAmbiguousNameError verifies that deleting by a bare leaf name
// that matches multiple groups returns an error rather than silently deleting one.
func TestGroupDeleteAmbiguousNameError(t *testing.T) {
	tree := session.NewGroupTreeWithGroups([]*session.Instance{}, nil)
	// Create pa, pb, then dup under each
	tree.CreateGroup("pa")
	tree.CreateGroup("pb")
	tree.CreateSubgroup("pa", "dup")
	tree.CreateSubgroup("pb", "dup")

	// Simulate the ambiguous-lookup logic from handleGroupDelete.
	name := "dup"
	type match struct {
		path  string
		group *session.Group
	}
	var matches []match
	for path, g := range tree.Groups {
		if strings.EqualFold(g.Name, name) {
			matches = append(matches, match{path: path, group: g})
		}
	}
	if len(matches) != 2 {
		t.Fatalf("expected 2 ambiguous matches for %q, got %d: %v", name, len(matches), matches)
	}
}

// groupKeys is a test helper that returns the keys of GroupTree.Groups.
func groupKeys(tree *session.GroupTree) []string {
	keys := make([]string, 0, len(tree.Groups))
	for k := range tree.Groups {
		keys = append(keys, k)
	}
	return keys
}
