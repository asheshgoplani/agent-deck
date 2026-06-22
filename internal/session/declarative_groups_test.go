package session

import "testing"

func emptyTree() *GroupTree {
	return NewGroupTree([]*Instance{})
}

// TestCanonicalGroupPathMatchesCreate verifies canonicalGroupPath predicts exactly the path CreateGroupPath stores.
func TestCanonicalGroupPathMatchesCreate(t *testing.T) {
	cases := []string{
		"projects",
		"Projects/DevOps",
		"work/My Api.",
		"a//b/",
		"weird@@name",
	}
	for _, key := range cases {
		tree := emptyTree()
		leaf := tree.CreateGroupPath(key)
		if leaf == nil {
			t.Fatalf("CreateGroupPath(%q) returned nil", key)
		}
		if got := canonicalGroupPath(key); got != leaf.Path {
			t.Errorf("canonicalGroupPath(%q) = %q, CreateGroupPath stored %q", key, got, leaf.Path)
		}
	}
}

// TestReconcileCreatesMissingGroupWithParents verifies a declared nested group is created along with its parents.
func TestReconcileCreatesMissingGroupWithParents(t *testing.T) {
	tree := emptyTree()
	cfg := &UserConfig{Groups: map[string]GroupSettings{"work/api": {Create: true}}}

	if !ReconcileDeclarativeGroups(tree, cfg) {
		t.Fatal("expected changed=true creating a new nested group")
	}
	if _, ok := tree.Groups["work"]; !ok {
		t.Error("parent group 'work' was not created")
	}
	if _, ok := tree.Groups["work/api"]; !ok {
		t.Error("leaf group 'work/api' was not created")
	}
}

// TestReconcileCreatesFlaggedGroupWithoutDefaultPath verifies create = true alone materializes a path-less group.
func TestReconcileCreatesFlaggedGroupWithoutDefaultPath(t *testing.T) {
	tree := emptyTree()
	cfg := &UserConfig{Groups: map[string]GroupSettings{"staging": {Create: true}}}

	if !ReconcileDeclarativeGroups(tree, cfg) {
		t.Fatal("expected changed=true creating a flagged group")
	}
	g, ok := tree.Groups["staging"]
	if !ok {
		t.Fatal("group 'staging' was not created")
	}
	if g.DefaultPath != "" {
		t.Errorf("DefaultPath = %q, want empty for a path-less group", g.DefaultPath)
	}
	if ReconcileDeclarativeGroups(tree, cfg) {
		t.Fatal("second run should report changed=false")
	}
}

// TestReconcileAppliesDefaultPath verifies a declared default_path is written to the created group.
func TestReconcileAppliesDefaultPath(t *testing.T) {
	dir := t.TempDir()
	tree := emptyTree()
	cfg := &UserConfig{Groups: map[string]GroupSettings{"proj": {Create: true, DefaultPath: dir}}}

	if !ReconcileDeclarativeGroups(tree, cfg) {
		t.Fatal("expected changed=true applying default_path")
	}
	if got := tree.Groups["proj"].DefaultPath; got != dir {
		t.Errorf("DefaultPath = %q, want %q", got, dir)
	}
}

// TestReconcileIsIdempotent verifies a second run with unchanged config reports no change.
func TestReconcileIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	tree := emptyTree()
	cfg := &UserConfig{Groups: map[string]GroupSettings{"proj": {Create: true, DefaultPath: dir}}}

	if !ReconcileDeclarativeGroups(tree, cfg) {
		t.Fatal("first run should report changed=true")
	}
	if ReconcileDeclarativeGroups(tree, cfg) {
		t.Fatal("second run should report changed=false")
	}
	if got := tree.Groups["proj"].DefaultPath; got != dir {
		t.Errorf("DefaultPath drifted to %q, want %q", got, dir)
	}
}

// TestReconcileDefaultPathAppliesToExistingGroup verifies default_path applies to an existing group without create = true.
func TestReconcileDefaultPathAppliesToExistingGroup(t *testing.T) {
	dir := t.TempDir()
	tree := emptyTree()
	tree.CreateGroup("proj")

	cfg := &UserConfig{Groups: map[string]GroupSettings{"proj": {DefaultPath: dir}}}
	if !ReconcileDeclarativeGroups(tree, cfg) {
		t.Fatal("expected changed=true applying default_path to an existing group")
	}
	if got := tree.Groups["proj"].DefaultPath; got != dir {
		t.Errorf("DefaultPath = %q, want %q", got, dir)
	}
}

// TestReconcileDefaultPathWithoutCreateSkipsMissingGroup verifies default_path without create = true does not create a missing group.
func TestReconcileDefaultPathWithoutCreateSkipsMissingGroup(t *testing.T) {
	dir := t.TempDir()
	tree := emptyTree()

	cfg := &UserConfig{Groups: map[string]GroupSettings{"ghost": {DefaultPath: dir}}}
	if ReconcileDeclarativeGroups(tree, cfg) {
		t.Fatal("expected changed=false: default_path alone must not create a group")
	}
	if _, ok := tree.Groups["ghost"]; ok {
		t.Error("group 'ghost' must not be created without create = true")
	}
}

// TestReconcileNoOpWhenMatching verifies no change is reported when the group and default_path already match.
func TestReconcileNoOpWhenMatching(t *testing.T) {
	dir := t.TempDir()
	tree := emptyTree()
	tree.CreateGroup("proj")
	tree.SetDefaultPathForGroup("proj", dir)

	cfg := &UserConfig{Groups: map[string]GroupSettings{"proj": {DefaultPath: dir}}}
	if ReconcileDeclarativeGroups(tree, cfg) {
		t.Fatal("expected changed=false when group and default_path already match")
	}
}

// TestReconcileMutatesAuthoritatively verifies config's default_path overwrites a different stored value.
func TestReconcileMutatesAuthoritatively(t *testing.T) {
	old := t.TempDir()
	want := t.TempDir()
	tree := emptyTree()
	tree.CreateGroup("proj")
	tree.SetDefaultPathForGroup("proj", old)

	cfg := &UserConfig{Groups: map[string]GroupSettings{"proj": {DefaultPath: want}}}
	if !ReconcileDeclarativeGroups(tree, cfg) {
		t.Fatal("expected changed=true overwriting an existing default_path")
	}
	if got := tree.Groups["proj"].DefaultPath; got != want {
		t.Errorf("DefaultPath = %q, want %q (config should win)", got, want)
	}
}

// TestReconcileDoesNotClearOmittedDefaultPath verifies an omitted default_path leaves the stored value intact.
func TestReconcileDoesNotClearOmittedDefaultPath(t *testing.T) {
	dir := t.TempDir()
	tree := emptyTree()
	tree.CreateGroup("proj")
	tree.SetDefaultPathForGroup("proj", dir)

	cfg := &UserConfig{Groups: map[string]GroupSettings{"proj": {}}}
	if ReconcileDeclarativeGroups(tree, cfg) {
		t.Fatal("expected changed=false: group exists and no default_path declared")
	}
	if got := tree.Groups["proj"].DefaultPath; got != dir {
		t.Errorf("DefaultPath was cleared to %q, want it retained as %q", got, dir)
	}
}

// TestReconcileNeverDeletes verifies a group absent from config is left in place.
func TestReconcileNeverDeletes(t *testing.T) {
	tree := emptyTree()
	tree.CreateGroup("keep")

	cfg := &UserConfig{Groups: map[string]GroupSettings{"proj": {Create: true}}}
	ReconcileDeclarativeGroups(tree, cfg)

	if _, ok := tree.Groups["keep"]; !ok {
		t.Error("group 'keep' absent from config must not be deleted")
	}
	if _, ok := tree.Groups["proj"]; !ok {
		t.Error("declared group 'proj' should have been created")
	}
}

// TestReconcileIgnoresToolOnlyGroupBlock verifies a .claude/.hermes-only block with no create flag creates nothing.
func TestReconcileIgnoresToolOnlyGroupBlock(t *testing.T) {
	tree := emptyTree()
	cfg := &UserConfig{Groups: map[string]GroupSettings{
		"conductor": {Claude: GroupClaudeSettings{ConfigDir: "~/.claude-work"}},
		"infra":     {Hermes: GroupHermesSettings{Command: "hermes"}},
	}}

	if ReconcileDeclarativeGroups(tree, cfg) {
		t.Fatal("tool-only group blocks must not report a change")
	}
	if _, ok := tree.Groups["conductor"]; ok {
		t.Error("claude-only block must not create the 'conductor' group")
	}
	if _, ok := tree.Groups["infra"]; ok {
		t.Error("hermes-only block must not create the 'infra' group")
	}
}

// TestReconcileNilSafe verifies nil tree or config inputs report no change.
func TestReconcileNilSafe(t *testing.T) {
	if ReconcileDeclarativeGroups(nil, &UserConfig{}) {
		t.Error("nil tree should report changed=false")
	}
	if ReconcileDeclarativeGroups(emptyTree(), nil) {
		t.Error("nil cfg should report changed=false")
	}
}

// TestCountFunctionalGroupsCountsDeclarativeFields verifies create and default_path mark a group functional for the section-drop guard.
func TestCountFunctionalGroupsCountsDeclarativeFields(t *testing.T) {
	groups := map[string]GroupSettings{
		"flagged":   {Create: true},
		"pathed":    {DefaultPath: "/tmp/x"},
		"tool-only": {Claude: GroupClaudeSettings{ConfigDir: "~/.c"}},
		"empty":     {},
	}
	if got := countFunctionalGroups(groups); got != 3 {
		t.Errorf("countFunctionalGroups = %d, want 3 (flagged, pathed, tool-only)", got)
	}
}
