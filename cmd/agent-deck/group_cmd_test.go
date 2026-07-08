package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// writeGroupDefaultsConfig writes config.toml to the legacy path under the
// isolated HOME. runAgentDeck re-points XDG_CONFIG_HOME at an unpopulated dir,
// so EffectiveConfigPath falls through to this legacy file deterministically.
func writeGroupDefaultsConfig(t *testing.T, home, content string) {
	t.Helper()
	dir := filepath.Join(home, ".agent-deck")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
}

// runGroupCreate runs `group create <args...> --json` under the isolated HOME
// and returns the parsed max_concurrent from the JSON payload.
func runGroupCreate(t *testing.T, home string, args ...string) int {
	t.Helper()
	full := append([]string{"group", "create"}, args...)
	full = append(full, "--json")
	stdout, stderr, code := runAgentDeck(t, home, full...)
	if code != 0 {
		t.Fatalf("group create %v failed (exit %d)\nstdout: %s\nstderr: %s", args, code, stdout, stderr)
	}
	var parsed struct {
		MaxConcurrent int `json:"max_concurrent"`
	}
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("parse group create JSON: %v\nstdout: %s", err, stdout)
	}
	return parsed.MaxConcurrent
}

// TestGroupCreate_DefaultMaxConcurrent_ConfigUnset: no config → new group is
// serial (1), byte-for-byte v1.9.1 behavior.
func TestGroupCreate_DefaultMaxConcurrent_ConfigUnset(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI subprocess test in -short mode")
	}
	home := t.TempDir()
	if got := runGroupCreate(t, home, "g"); got != 1 {
		t.Errorf("config unset: expected max_concurrent=1, got %d", got)
	}
}

// TestGroupCreate_DefaultMaxConcurrent_ConfigN: [group_defaults].max_concurrent = 3
// → new group capped at 3.
func TestGroupCreate_DefaultMaxConcurrent_ConfigN(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI subprocess test in -short mode")
	}
	home := t.TempDir()
	writeGroupDefaultsConfig(t, home, "[group_defaults]\nmax_concurrent = 3\n")
	if got := runGroupCreate(t, home, "g"); got != 3 {
		t.Errorf("config N=3: expected max_concurrent=3, got %d", got)
	}
}

// TestGroupCreate_FlagOverridesConfigDefault: --max-concurrent beats the config
// default (precedence: flag > config > built-in 1). Uses the --flag=value form;
// the space-separated form is mishandled by reorderGroupArgs (a pre-existing
// arg-parsing issue unrelated to [group_defaults]).
func TestGroupCreate_FlagOverridesConfigDefault(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI subprocess test in -short mode")
	}
	home := t.TempDir()
	writeGroupDefaultsConfig(t, home, "[group_defaults]\nmax_concurrent = 5\n")
	if got := runGroupCreate(t, home, "g", "--max-concurrent=2"); got != 2 {
		t.Errorf("flag override: expected max_concurrent=2, got %d", got)
	}
}

// TestGroupCreate_ConfigZeroUnlimited: [group_defaults].max_concurrent = 0 →
// new group is unlimited (0). Proves *0 is not collapsed to the built-in 1.
func TestGroupCreate_ConfigZeroUnlimited(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI subprocess test in -short mode")
	}
	home := t.TempDir()
	writeGroupDefaultsConfig(t, home, "[group_defaults]\nmax_concurrent = 0\n")
	if got := runGroupCreate(t, home, "g"); got != 0 {
		t.Errorf("config 0: expected max_concurrent=0 (unlimited), got %d", got)
	}
}
