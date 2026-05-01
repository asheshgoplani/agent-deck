package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
	"github.com/stretchr/testify/assert"
)

func TestNewGroupTree(t *testing.T) {
	instances := []*Instance{
		{ID: "1", Title: "session-1", GroupPath: "project-a"},
		{ID: "2", Title: "session-2", GroupPath: "project-a"},
		{ID: "3", Title: "session-3", GroupPath: "project-b"},
	}

	tree := NewGroupTree(instances)

	if tree.GroupCount() != 2 {
		t.Errorf("Expected 2 groups, got %d", tree.GroupCount())
	}

	if tree.SessionCount() != 3 {
		t.Errorf("Expected 3 sessions, got %d", tree.SessionCount())
	}

	// Check group contents
	groupA := tree.Groups["project-a"]
	if groupA == nil {
		t.Fatal("project-a group not found")
	}
	if len(groupA.Sessions) != 2 {
		t.Errorf("Expected 2 sessions in project-a, got %d", len(groupA.Sessions))
	}
}

func TestNewGroupTreeEmptyGroupPath(t *testing.T) {
	instances := []*Instance{
		{ID: "1", Title: "session-1", GroupPath: ""},
	}

	tree := NewGroupTree(instances)

	// Empty group path should default to DefaultGroupPath
	defaultGroup := tree.Groups[DefaultGroupPath]
	if defaultGroup == nil {
		t.Fatalf("default group '%s' not found", DefaultGroupPath)
	}
	if len(defaultGroup.Sessions) != 1 {
		t.Errorf("Expected 1 session in default, got %d", len(defaultGroup.Sessions))
	}
}

func TestCreateGroup(t *testing.T) {
	tree := NewGroupTree([]*Instance{})

	group := tree.CreateGroup("My Project")

	if group == nil {
		t.Fatal("CreateGroup returned nil")
	}
	if group.Name != "My Project" {
		t.Errorf("Expected name 'My Project', got '%s'", group.Name)
	}
	if group.Path != "My-Project" {
		t.Errorf("Expected path 'My-Project', got '%s'", group.Path)
	}
	if !group.Expanded {
		t.Error("New group should be expanded by default")
	}
	if tree.GroupCount() != 1 {
		t.Errorf("Expected 1 group, got %d", tree.GroupCount())
	}
}

func TestCreateSubgroup(t *testing.T) {
	tree := NewGroupTree([]*Instance{})

	// Create parent group
	parent := tree.CreateGroup("Parent")
	if parent == nil {
		t.Fatal("CreateGroup returned nil")
	}

	// Create subgroup
	child := tree.CreateSubgroup("Parent", "Child")
	if child == nil {
		t.Fatal("CreateSubgroup returned nil")
	}

	if child.Name != "Child" {
		t.Errorf("Expected name 'Child', got '%s'", child.Name)
	}
	if child.Path != "Parent/Child" {
		t.Errorf("Expected path 'Parent/Child', got '%s'", child.Path)
	}
	if tree.GroupCount() != 2 {
		t.Errorf("Expected 2 groups, got %d", tree.GroupCount())
	}
}

func TestCreateNestedSubgroups(t *testing.T) {
	tree := NewGroupTree([]*Instance{})

	// Create hierarchy: grandparent -> parent -> child
	tree.CreateGroup("Grandparent")
	tree.CreateSubgroup("Grandparent", "Parent")
	tree.CreateSubgroup("Grandparent/Parent", "Child")

	if tree.GroupCount() != 3 {
		t.Errorf("Expected 3 groups, got %d", tree.GroupCount())
	}

	child := tree.Groups["Grandparent/Parent/Child"]
	if child == nil {
		t.Fatal("Nested child group not found")
	}
	if child.Path != "Grandparent/Parent/Child" {
		t.Errorf("Expected path 'Grandparent/Parent/Child', got '%s'", child.Path)
	}
}

func TestGetGroupLevel(t *testing.T) {
	tests := []struct {
		path     string
		expected int
	}{
		{"", 0},
		{"root", 0},
		{"parent/child", 1},
		{"a/b/c", 2},
		{"a/b/c/d", 3},
	}

	for _, tt := range tests {
		level := GetGroupLevel(tt.path)
		if level != tt.expected {
			t.Errorf("GetGroupLevel(%s) = %d, want %d", tt.path, level, tt.expected)
		}
	}
}

func TestFlatten(t *testing.T) {
	instances := []*Instance{
		{ID: "1", Title: "session-1", GroupPath: "group-a"},
		{ID: "2", Title: "session-2", GroupPath: "group-b"},
	}

	tree := NewGroupTree(instances)
	items := tree.Flatten()

	// Should have 2 groups + 2 sessions = 4 items
	if len(items) != 4 {
		t.Errorf("Expected 4 items, got %d", len(items))
	}

	// First item should be a group
	if items[0].Type != ItemTypeGroup {
		t.Error("First item should be a group")
	}
}

func TestFlattenWithCollapsedGroup(t *testing.T) {
	instances := []*Instance{
		{ID: "1", Title: "session-1", GroupPath: "group-a"},
		{ID: "2", Title: "session-2", GroupPath: "group-a"},
	}

	tree := NewGroupTree(instances)

	// Collapse the group
	tree.CollapseGroup("group-a")

	items := tree.Flatten()

	// Should have 1 group only (sessions hidden)
	if len(items) != 1 {
		t.Errorf("Expected 1 item (collapsed group), got %d", len(items))
	}
}

func TestFlattenWithNestedGroupsCollapsed(t *testing.T) {
	tree := NewGroupTree([]*Instance{})

	// Create hierarchy
	tree.CreateGroup("Parent")
	tree.CreateSubgroup("Parent", "Child")

	// Add sessions
	tree.Groups["Parent"].Sessions = []*Instance{{ID: "1", GroupPath: "Parent"}}
	tree.Groups["Parent/Child"].Sessions = []*Instance{{ID: "2", GroupPath: "Parent/Child"}}

	// Expand all first
	tree.ExpandGroup("Parent")
	tree.ExpandGroup("Parent/Child")

	items := tree.Flatten()
	// parent(group) + session + child(group) + session = 4
	if len(items) != 4 {
		t.Errorf("Expected 4 items when expanded, got %d", len(items))
	}

	// Collapse parent - should hide child group and all sessions
	tree.CollapseGroup("Parent")
	items = tree.Flatten()

	// Only parent group visible
	if len(items) != 1 {
		t.Errorf("Expected 1 item when parent collapsed, got %d", len(items))
	}
}

// TestSubgroupSortingWithUnrelatedRoots verifies that subgroups stay with their
// parent root and are not sorted between unrelated root groups.
// This was a bug where "agent-deck/github-issues" would sort between "My Sessions"
// and "agent-deck" because full path comparison doesn't respect tree hierarchy.
func TestSubgroupSortingWithUnrelatedRoots(t *testing.T) {
	tree := NewGroupTree([]*Instance{})

	// Create root groups with names that alphabetically interleave
	// "My Sessions" (M) < "agent-deck" (a) in ASCII (uppercase < lowercase)
	// But "agent-deck/github-issues" would sort before "my-sessions" by full path
	tree.CreateGroup("My Sessions") // path: My-Sessions
	tree.CreateGroup("agent-deck")  // path: agent-deck
	tree.CreateGroup("ard")         // path: ard
	tree.CreateSubgroup("agent-deck", "github-issues")

	// Expand all so subgroups are visible
	tree.ExpandGroup("My-Sessions")
	tree.ExpandGroup("agent-deck")
	tree.ExpandGroup("ard")

	// Flatten the tree
	items := tree.Flatten()

	// Find positions of each group
	positions := make(map[string]int)
	for i, item := range items {
		if item.Type == ItemTypeGroup {
			positions[item.Path] = i
		}
	}

	// Verify: github-issues must come immediately after agent-deck, not before my-sessions
	agentDeckPos := positions["agent-deck"]
	githubIssuesPos := positions["agent-deck/github-issues"]
	mySessionsPos := positions["My-Sessions"]
	ardPos := positions["ard"]

	// agent-deck/github-issues should come right after agent-deck
	if githubIssuesPos != agentDeckPos+1 {
		t.Errorf("github-issues (pos %d) should come right after agent-deck (pos %d)",
			githubIssuesPos, agentDeckPos)
	}

	// my-sessions should NOT be between agent-deck and github-issues
	if mySessionsPos > agentDeckPos && mySessionsPos < githubIssuesPos {
		t.Errorf("my-sessions (pos %d) should not be between agent-deck (pos %d) and github-issues (pos %d)",
			mySessionsPos, agentDeckPos, githubIssuesPos)
	}

	// ard should come after both agent-deck and github-issues (same root family, then ard)
	if ardPos < githubIssuesPos {
		t.Errorf("ard (pos %d) should come after github-issues (pos %d)",
			ardPos, githubIssuesPos)
	}
}

func TestToggleGroup(t *testing.T) {
	tree := NewGroupTree([]*Instance{})
	tree.CreateGroup("Test")

	// Initially expanded
	if !tree.Groups["Test"].Expanded {
		t.Error("Group should be expanded initially")
	}

	// Toggle to collapse
	tree.ToggleGroup("Test")
	if tree.Groups["Test"].Expanded {
		t.Error("Group should be collapsed after toggle")
	}

	// Toggle to expand
	tree.ToggleGroup("Test")
	if !tree.Groups["Test"].Expanded {
		t.Error("Group should be expanded after second toggle")
	}
}

func TestExpandGroupWithParents(t *testing.T) {
	// Create a tree with nested groups
	instances := []*Instance{
		{ID: "1", Title: "deep-session", GroupPath: "parent/child/grandchild"},
	}

	tree := NewGroupTree(instances)

	// Create parent and child groups explicitly
	tree.CreateGroup("parent")
	tree.CreateSubgroup("parent", "child")
	tree.CreateSubgroup("parent/child", "grandchild")

	// Collapse all groups
	tree.CollapseGroup("parent")
	tree.CollapseGroup("parent/child")
	tree.CollapseGroup("parent/child/grandchild")

	// Verify all collapsed
	if tree.Groups["parent"].Expanded {
		t.Error("parent should be collapsed")
	}
	if tree.Groups["parent/child"].Expanded {
		t.Error("parent/child should be collapsed")
	}

	// Now expand with parents
	tree.ExpandGroupWithParents("parent/child/grandchild")

	// All should be expanded now
	if !tree.Groups["parent"].Expanded {
		t.Error("parent should be expanded after ExpandGroupWithParents")
	}
	if !tree.Groups["parent/child"].Expanded {
		t.Error("parent/child should be expanded after ExpandGroupWithParents")
	}
	if !tree.Groups["parent/child/grandchild"].Expanded {
		t.Error("parent/child/grandchild should be expanded after ExpandGroupWithParents")
	}

	// Verify session is now visible in flattened view
	items := tree.Flatten()
	foundSession := false
	for _, item := range items {
		if item.Type == ItemTypeSession && item.Session != nil && item.Session.ID == "1" {
			foundSession = true
			break
		}
	}
	if !foundSession {
		t.Error("Session should be visible in flattened view after ExpandGroupWithParents")
	}
}

func TestRenameGroup(t *testing.T) {
	instances := []*Instance{
		{ID: "1", Title: "session-1", GroupPath: "old-name"},
	}

	tree := NewGroupTree(instances)
	tree.RenameGroup("old-name", "New Name")

	// Old group should not exist
	if tree.Groups["old-name"] != nil {
		t.Error("Old group should be removed")
	}

	// New group should exist
	newGroup := tree.Groups["New-Name"]
	if newGroup == nil {
		t.Fatal("New group not found")
	}

	if newGroup.Name != "New Name" {
		t.Errorf("Expected name 'New Name', got '%s'", newGroup.Name)
	}

	// Session should be updated
	if instances[0].GroupPath != "New-Name" {
		t.Errorf("Session GroupPath not updated, got '%s'", instances[0].GroupPath)
	}
}

func TestRenameGroupWithSubgroups(t *testing.T) {
	tree := NewGroupTree([]*Instance{})

	// Create hierarchy
	tree.CreateGroup("Parent")
	tree.CreateSubgroup("Parent", "Child")
	tree.CreateSubgroup("Parent/Child", "Grandchild")

	// Add sessions to each
	tree.Groups["Parent"].Sessions = []*Instance{{ID: "1", GroupPath: "Parent"}}
	tree.Groups["Parent/Child"].Sessions = []*Instance{{ID: "2", GroupPath: "Parent/Child"}}
	tree.Groups["Parent/Child/Grandchild"].Sessions = []*Instance{{ID: "3", GroupPath: "Parent/Child/Grandchild"}}

	// Rename parent
	tree.RenameGroup("Parent", "NewParent")

	// Verify old paths don't exist
	if tree.Groups["Parent"] != nil {
		t.Error("Old parent path should not exist")
	}
	if tree.Groups["Parent/Child"] != nil {
		t.Error("Old child path should not exist")
	}
	if tree.Groups["Parent/Child/Grandchild"] != nil {
		t.Error("Old grandchild path should not exist")
	}

	// Verify new paths exist
	if tree.Groups["NewParent"] == nil {
		t.Error("New parent path should exist")
	}
	if tree.Groups["NewParent/Child"] == nil {
		t.Error("New child path should exist")
	}
	if tree.Groups["NewParent/Child/Grandchild"] == nil {
		t.Error("New grandchild path should exist")
	}

	// Verify session GroupPaths updated
	if tree.Groups["NewParent"].Sessions[0].GroupPath != "NewParent" {
		t.Error("Parent session GroupPath not updated")
	}
	if tree.Groups["NewParent/Child"].Sessions[0].GroupPath != "NewParent/Child" {
		t.Error("Child session GroupPath not updated")
	}
	if tree.Groups["NewParent/Child/Grandchild"].Sessions[0].GroupPath != "NewParent/Child/Grandchild" {
		t.Error("Grandchild session GroupPath not updated")
	}
}

// TestRenameSubgroup verifies that renaming a subgroup keeps it under its parent.
// This was a bug where renaming "parent/child" to "NewChild" would result in path "NewChild"
// instead of "parent/NewChild", effectively moving the group to root level.
func TestRenameSubgroup(t *testing.T) {
	tree := NewGroupTree([]*Instance{})

	// Create hierarchy: project-a -> task-b
	tree.CreateGroup("Project A")
	tree.CreateSubgroup("Project-A", "Task B")

	// Add a session to the subgroup
	session := &Instance{ID: "1", Title: "my-session", GroupPath: "Project-A/Task-B"}
	tree.Groups["Project-A/Task-B"].Sessions = []*Instance{session}

	// Verify initial structure
	if tree.Groups["Project-A/Task-B"] == nil {
		t.Fatal("Subgroup Project-A/Task-B should exist")
	}

	// Rename the subgroup from "Task B" to "Task C"
	tree.RenameGroup("Project-A/Task-B", "Task C")

	// OLD path should NOT exist
	if tree.Groups["Project-A/Task-B"] != nil {
		t.Error("Old path Project-A/Task-B should not exist after rename")
	}

	// NEW path should be "Project-A/Task-C" (preserved parent), NOT "Task-C" (root level)
	if tree.Groups["Task-C"] != nil {
		t.Error("Bug: Renamed subgroup should NOT be at root level (Task-C)")
	}
	renamedGroup := tree.Groups["Project-A/Task-C"]
	if renamedGroup == nil {
		t.Fatal("Renamed subgroup should be at Project-A/Task-C")
	}

	// Verify the group properties
	if renamedGroup.Name != "Task C" {
		t.Errorf("Expected name 'Task C', got '%s'", renamedGroup.Name)
	}
	if renamedGroup.Path != "Project-A/Task-C" {
		t.Errorf("Expected path 'Project-A/Task-C', got '%s'", renamedGroup.Path)
	}

	// Verify session GroupPath was updated
	if session.GroupPath != "Project-A/Task-C" {
		t.Errorf("Session GroupPath should be 'Project-A/Task-C', got '%s'", session.GroupPath)
	}

	// Verify parent group still exists and is unaffected
	parentGroup := tree.Groups["Project-A"]
	if parentGroup == nil {
		t.Fatal("Parent group Project-A should still exist")
	}
	if parentGroup.Name != "Project A" {
		t.Errorf("Parent name should be 'Project A', got '%s'", parentGroup.Name)
	}
}

func TestDeleteGroup(t *testing.T) {
	instances := []*Instance{
		{ID: "1", Title: "session-1", GroupPath: "to-delete"},
	}

	tree := NewGroupTree(instances)

	// Note: DeleteGroup creates the default group if it doesn't exist
	// when it moves sessions there

	movedSessions := tree.DeleteGroup("to-delete")

	// Group should be removed
	if tree.Groups["to-delete"] != nil {
		t.Error("Deleted group should not exist")
	}

	// Session should be moved to default
	if len(movedSessions) != 1 {
		t.Errorf("Expected 1 moved session, got %d", len(movedSessions))
	}
	if movedSessions[0].GroupPath != DefaultGroupPath {
		t.Errorf("Session should be moved to %s, got '%s'", DefaultGroupPath, movedSessions[0].GroupPath)
	}
}

func TestDeleteGroupWithSubgroups(t *testing.T) {
	tree := NewGroupTree([]*Instance{})

	// Create hierarchy
	tree.CreateGroup("Parent")
	tree.CreateSubgroup("Parent", "Child")

	// Note: DeleteGroup creates the default group if it doesn't exist
	// when it moves sessions there

	// Add sessions
	tree.Groups["Parent"].Sessions = []*Instance{{ID: "1", GroupPath: "Parent"}}
	tree.Groups["Parent/Child"].Sessions = []*Instance{{ID: "2", GroupPath: "Parent/Child"}}

	// Delete parent - should cascade to child
	movedSessions := tree.DeleteGroup("Parent")

	// Both groups should be removed
	if tree.Groups["Parent"] != nil {
		t.Error("Parent group should be deleted")
	}
	if tree.Groups["Parent/Child"] != nil {
		t.Error("Child group should be deleted")
	}

	// Both sessions should be moved to default
	if len(movedSessions) != 2 {
		t.Errorf("Expected 2 moved sessions, got %d", len(movedSessions))
	}

	for _, sess := range movedSessions {
		if sess.GroupPath != DefaultGroupPath {
			t.Errorf("Session should be moved to %s, got '%s'", DefaultGroupPath, sess.GroupPath)
		}
	}
}

func TestDeleteDefaultGroup(t *testing.T) {
	// Create a session with empty GroupPath - this auto-creates the default group
	instances := []*Instance{
		{ID: "1", Title: "session-1", GroupPath: ""},
	}
	tree := NewGroupTree(instances)

	// Verify default group was created (uses normalized path now)
	if tree.Groups[DefaultGroupPath] == nil {
		t.Fatalf("Default group '%s' should exist after creating session with empty GroupPath", DefaultGroupPath)
	}

	// Should not be able to delete default
	result := tree.DeleteGroup(DefaultGroupPath)
	if result != nil {
		t.Error("Should not be able to delete default group")
	}
	if tree.Groups[DefaultGroupPath] == nil {
		t.Errorf("Default group '%s' should still exist after delete attempt", DefaultGroupPath)
	}
}

// TestDeleteEmptyGroupDoesNotCreateDefault verifies that deleting an empty,
// non-default group does NOT spuriously create the default "My Sessions" group
// when no sessions need to be moved.
func TestDeleteEmptyGroupDoesNotCreateDefault(t *testing.T) {
	tree := NewGroupTree([]*Instance{})
	tree.CreateGroup("Experiments")

	// Sanity: default group should not exist (no ungrouped sessions)
	if _, exists := tree.Groups[DefaultGroupPath]; exists {
		t.Fatalf("precondition failed: default group should not exist before delete")
	}

	moved := tree.DeleteGroup("experiments")

	if len(moved) != 0 {
		t.Errorf("expected 0 moved sessions, got %d", len(moved))
	}
	if tree.Groups["experiments"] != nil {
		t.Error("deleted group should be gone")
	}
	if _, exists := tree.Groups[DefaultGroupPath]; exists {
		t.Errorf("default group '%s' should NOT have been created when deleting an empty group", DefaultGroupPath)
	}
}

func TestMoveSessionToGroup(t *testing.T) {
	instances := []*Instance{
		{ID: "1", Title: "session-1", GroupPath: "source"},
	}

	tree := NewGroupTree(instances)
	tree.CreateGroup("target")

	tree.MoveSessionToGroup(instances[0], "target")

	// Session should be in target group
	if instances[0].GroupPath != "target" {
		t.Errorf("Session GroupPath not updated, got '%s'", instances[0].GroupPath)
	}

	// Source group should be empty (but still exist)
	if len(tree.Groups["source"].Sessions) != 0 {
		t.Error("Source group should be empty")
	}

	// Target group should have the session
	if len(tree.Groups["target"].Sessions) != 1 {
		t.Error("Target group should have 1 session")
	}
}

func TestGroupDefaultPath(t *testing.T) {
	now := time.Now()

	instances := []*Instance{
		{ID: "1", Title: "old-session", GroupPath: "projects", ProjectPath: "/old/path", LastAccessedAt: now.Add(-1 * time.Hour)},
		{ID: "2", Title: "new-session", GroupPath: "projects", ProjectPath: "/new/path", LastAccessedAt: now},
		{ID: "3", Title: "other-session", GroupPath: "other", ProjectPath: "/other/path", LastAccessedAt: now},
	}

	tree := NewGroupTree(instances)

	// Check that effective default path resolves from most recent session.
	if got := tree.DefaultPathForGroup("projects"); got != "/new/path" {
		t.Errorf("Expected default path '/new/path', got '%s'", got)
	}
	if got := tree.DefaultPathForGroup("other"); got != "/other/path" {
		t.Errorf("Expected default path '/other/path', got '%s'", got)
	}
}

func TestGroupDefaultPathOnMove(t *testing.T) {
	now := time.Now()

	instances := []*Instance{
		{ID: "1", Title: "session-1", GroupPath: "source", ProjectPath: "/source/path", LastAccessedAt: now},
	}

	tree := NewGroupTree(instances)
	tree.CreateGroup("target")

	// Move session to target group
	tree.MoveSessionToGroup(instances[0], "target")

	// Target group should resolve to the moved session's path.
	if got := tree.DefaultPathForGroup("target"); got != "/source/path" {
		t.Errorf("Expected target default path '/source/path', got '%s'", got)
	}
}

func TestGroupDefaultPathPersistence(t *testing.T) {
	now := time.Now()

	// Simulate stored groups with default path
	storedGroups := []*GroupData{
		{Name: "Projects", Path: "projects", Expanded: true, Order: 0, DefaultPath: "/stored/path"},
	}

	// Create instances with older path
	instances := []*Instance{
		{ID: "1", Title: "session-1", GroupPath: "projects", ProjectPath: "/newer/path", LastAccessedAt: now},
	}

	tree := NewGroupTreeWithGroups(instances, storedGroups)

	// Explicit stored default path should be preserved.
	if got := tree.DefaultPathForGroup("projects"); got != "/stored/path" {
		t.Errorf("Expected default path '/stored/path', got '%s'", got)
	}
}

func TestSetDefaultPathForGroup(t *testing.T) {
	tree := NewGroupTree([]*Instance{})
	tree.CreateGroup("Projects")

	if ok := tree.SetDefaultPathForGroup("Projects", "/tmp/project-root"); !ok {
		t.Fatal("SetDefaultPathForGroup should return true for existing group")
	}

	if got := tree.DefaultPathForGroup("Projects"); got != "/tmp/project-root" {
		t.Fatalf("Expected explicit default path '/tmp/project-root', got %q", got)
	}

	if ok := tree.SetDefaultPathForGroup("Projects", ""); !ok {
		t.Fatal("SetDefaultPathForGroup should allow clearing")
	}

	if got := tree.DefaultPathForGroup("Projects"); got != "" {
		t.Fatalf("Expected empty default path after clear, got %q", got)
	}
}

func TestDefaultPathForGroupResolvesWorktreeToRepoRoot(t *testing.T) {
	// Skip if git is unavailable in test environment.
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	wtDir := filepath.Join(tmpDir, "repo-worktree")

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = testutil.CleanGitEnv(os.Environ())
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	run("init", repoDir)
	run("-C", repoDir, "config", "user.email", "test@example.com")
	run("-C", repoDir, "config", "user.name", "Test User")

	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("failed to write repo file: %v", err)
	}
	run("-C", repoDir, "add", "README.md")
	run("-C", repoDir, "commit", "-m", "init")
	run("-C", repoDir, "worktree", "add", wtDir, "-b", "feature/test")

	instances := []*Instance{
		{
			ID:             "1",
			Title:          "worktree-session",
			GroupPath:      "projects",
			ProjectPath:    wtDir,
			LastAccessedAt: time.Now(),
		},
	}

	tree := NewGroupTree(instances)
	got := tree.DefaultPathForGroup("projects")

	realRepoDir, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		realRepoDir = repoDir
	}
	realGot, err := filepath.EvalSymlinks(got)
	if err != nil {
		realGot = got
	}

	if realGot != realRepoDir {
		t.Fatalf("Expected default path to resolve to repo root %q, got %q", realRepoDir, realGot)
	}
}

func TestMoveGroupUpDownSiblings(t *testing.T) {
	tree := NewGroupTree([]*Instance{})

	// Create sibling groups
	tree.CreateGroup("Alpha")
	tree.CreateGroup("Beta")
	tree.CreateGroup("Gamma")

	// Initial order: Alpha, Beta, Gamma
	if tree.GroupList[0].Path != "Alpha" {
		t.Errorf("Expected Alpha first, got %s", tree.GroupList[0].Path)
	}

	// Move Beta up - should swap with Alpha
	tree.MoveGroupUp("Beta")
	if tree.GroupList[0].Path != "Beta" {
		t.Errorf("Expected Beta first after move up, got %s", tree.GroupList[0].Path)
	}

	// Move Beta down - should swap with Alpha
	tree.MoveGroupDown("Beta")
	if tree.GroupList[1].Path != "Beta" {
		t.Errorf("Expected Beta second after move down, got %s", tree.GroupList[1].Path)
	}
}

func TestMoveGroupNotAcrossLevels(t *testing.T) {
	tree := NewGroupTree([]*Instance{})

	// Create parent and child
	tree.CreateGroup("Parent")
	tree.CreateSubgroup("Parent", "Child")

	// Try to move child up - should not swap with parent (different levels)
	initialOrder := make([]string, len(tree.GroupList))
	for i, g := range tree.GroupList {
		initialOrder[i] = g.Path
	}

	tree.MoveGroupUp("Parent/Child")

	// Order should be unchanged (can't move child above parent)
	for i, g := range tree.GroupList {
		if g.Path != initialOrder[i] {
			t.Errorf("Group order should not change when moving across levels")
			break
		}
	}
}

func TestAddSession(t *testing.T) {
	tree := NewGroupTree([]*Instance{})
	tree.CreateGroup("test")

	inst := &Instance{ID: "1", Title: "new-session", GroupPath: "test"}
	tree.AddSession(inst)

	if len(tree.Groups["test"].Sessions) != 1 {
		t.Error("Session should be added to group")
	}
}

func TestRemoveSession(t *testing.T) {
	instances := []*Instance{
		{ID: "1", Title: "session-1", GroupPath: "test"},
	}

	tree := NewGroupTree(instances)

	tree.RemoveSession(instances[0])

	if len(tree.Groups["test"].Sessions) != 0 {
		t.Error("Session should be removed from group")
	}

	// Group should still exist (empty groups persist)
	if tree.Groups["test"] == nil {
		t.Error("Empty group should persist")
	}
}

func TestGetGroupNames(t *testing.T) {
	tree := NewGroupTree([]*Instance{})
	tree.CreateGroup("Alpha")
	tree.CreateGroup("Beta")

	names := tree.GetGroupNames()

	if len(names) != 2 {
		t.Errorf("Expected 2 names, got %d", len(names))
	}
}

func TestGetGroupPaths(t *testing.T) {
	tree := NewGroupTree([]*Instance{})
	tree.CreateGroup("Alpha")
	tree.CreateSubgroup("Alpha", "Child")

	paths := tree.GetGroupPaths()

	if len(paths) != 2 {
		t.Errorf("Expected 2 paths, got %d", len(paths))
	}

	// Check paths contain expected values
	foundAlpha := false
	foundChild := false
	for _, p := range paths {
		if p == "Alpha" {
			foundAlpha = true
		}
		if p == "Alpha/Child" {
			foundChild = true
		}
	}

	if !foundAlpha || !foundChild {
		t.Error("Expected both Alpha and Alpha/Child paths")
	}
}

func TestSyncWithInstances(t *testing.T) {
	tree := NewGroupTree([]*Instance{})
	tree.CreateGroup("persistent")
	tree.CreateGroup("another")

	// Add some sessions
	oldInstances := []*Instance{
		{ID: "1", Title: "old-session", GroupPath: "persistent"},
	}
	for _, inst := range oldInstances {
		tree.AddSession(inst)
	}

	// Sync with new instances (simulating refresh)
	newInstances := []*Instance{
		{ID: "2", Title: "new-session", GroupPath: "persistent"},
		{ID: "3", Title: "another-session", GroupPath: "another"},
	}

	tree.SyncWithInstances(newInstances)

	// Both groups should still exist
	if tree.Groups["persistent"] == nil {
		t.Error("persistent group should exist")
	}
	if tree.Groups["another"] == nil {
		t.Error("another group should exist")
	}

	// Sessions should be updated
	if len(tree.Groups["persistent"].Sessions) != 1 {
		t.Errorf("Expected 1 session in persistent, got %d", len(tree.Groups["persistent"].Sessions))
	}
	if tree.Groups["persistent"].Sessions[0].ID != "2" {
		t.Error("Session should be the new one")
	}
}

func TestNewGroupTreeWithGroups(t *testing.T) {
	instances := []*Instance{
		{ID: "1", Title: "session-1", GroupPath: "existing"},
	}

	storedGroups := []*GroupData{
		{Name: "existing", Path: "existing", Expanded: true, Order: 0},
		{Name: "empty-group", Path: "empty-group", Expanded: false, Order: 1},
	}

	tree := NewGroupTreeWithGroups(instances, storedGroups)

	// Both groups should exist
	if tree.Groups["existing"] == nil {
		t.Error("existing group should exist")
	}
	if tree.Groups["empty-group"] == nil {
		t.Error("empty-group should exist (persisted)")
	}

	// Empty group should have no sessions but exist
	if len(tree.Groups["empty-group"].Sessions) != 0 {
		t.Error("empty-group should have no sessions")
	}

	// Expanded state should be preserved
	if tree.Groups["empty-group"].Expanded {
		t.Error("empty-group should be collapsed (as stored)")
	}
}

func TestGetParentPath(t *testing.T) {
	tests := []struct {
		path     string
		expected string
	}{
		{"root", ""},
		{"parent/child", "parent"},
		{"a/b/c", "a/b"},
		{"", ""},
	}

	for _, tt := range tests {
		result := getParentPath(tt.path)
		if result != tt.expected {
			t.Errorf("getParentPath(%s) = %s, want %s", tt.path, result, tt.expected)
		}
	}
}

func TestGetRootPath(t *testing.T) {
	tests := []struct {
		path     string
		expected string
	}{
		{"root", "root"},
		{"parent/child", "parent"},
		{"a/b/c", "a"},
		{"my-sessions", "my-sessions"},
		{"agent-deck/github-issues", "agent-deck"},
		{"deep/nested/path/here", "deep"},
	}

	for _, tt := range tests {
		result := getRootPath(tt.path)
		if result != tt.expected {
			t.Errorf("getRootPath(%s) = %s, want %s", tt.path, result, tt.expected)
		}
	}
}

func TestExtractGroupName(t *testing.T) {
	tests := []struct {
		path     string
		expected string
	}{
		{"root", "root"},
		{"parent/child", "child"},
		{"a/b/c", "c"},
		{"", ""},
		{"my-sessions", "my-sessions"},
		{"ard/innotrade", "innotrade"},
	}

	for _, tt := range tests {
		result := extractGroupName(tt.path)
		if result != tt.expected {
			t.Errorf("extractGroupName(%s) = %s, want %s", tt.path, result, tt.expected)
		}
	}
}

func TestNewGroupTreeWithHierarchicalPath(t *testing.T) {
	// Simulate session created with hierarchical group path
	instances := []*Instance{
		{ID: "1", Title: "session-1", GroupPath: "parent/child"},
	}

	tree := NewGroupTree(instances)

	// Group should exist with correct name
	group := tree.Groups["parent/child"]
	if group == nil {
		t.Fatal("parent/child group not found")
	}

	// Name should be just "child", not "parent/child"
	if group.Name != "child" {
		t.Errorf("Expected name 'child', got '%s'", group.Name)
	}

	// Path should be full path
	if group.Path != "parent/child" {
		t.Errorf("Expected path 'parent/child', got '%s'", group.Path)
	}
}

func TestNewGroupTreeWithGroupsHierarchicalPath(t *testing.T) {
	// Session has hierarchical group path not in stored groups
	instances := []*Instance{
		{ID: "1", Title: "session-1", GroupPath: "ard/innotrade"},
	}

	// Stored groups don't include the new hierarchical group
	storedGroups := []*GroupData{
		{Name: "ard", Path: "ard", Expanded: true, Order: 0},
	}

	tree := NewGroupTreeWithGroups(instances, storedGroups)

	// New group should be auto-created with correct name
	group := tree.Groups["ard/innotrade"]
	if group == nil {
		t.Fatal("ard/innotrade group not found")
	}

	// Name should be just "innotrade", not "ard/innotrade"
	if group.Name != "innotrade" {
		t.Errorf("Expected name 'innotrade', got '%s'", group.Name)
	}
}

func TestAddSessionWithHierarchicalPath(t *testing.T) {
	tree := NewGroupTree([]*Instance{})

	// Create parent group first
	tree.CreateGroup("parent")

	// Add session with hierarchical path
	inst := &Instance{ID: "1", Title: "session-1", GroupPath: "parent/child"}
	tree.AddSession(inst)

	// New group should be auto-created with correct name
	group := tree.Groups["parent/child"]
	if group == nil {
		t.Fatal("parent/child group not found")
	}

	// Name should be just "child", not "parent/child"
	if group.Name != "child" {
		t.Errorf("Expected name 'child', got '%s'", group.Name)
	}

	// Session should be in the group
	if len(group.Sessions) != 1 {
		t.Errorf("Expected 1 session, got %d", len(group.Sessions))
	}
}

func TestSyncWithInstancesHierarchicalPath(t *testing.T) {
	// Start with empty tree
	tree := NewGroupTree([]*Instance{})

	// Sync with instances that have hierarchical paths
	instances := []*Instance{
		{ID: "1", Title: "session-1", GroupPath: "projects/backend"},
	}
	tree.SyncWithInstances(instances)

	// Group should be created with correct name
	group := tree.Groups["projects/backend"]
	if group == nil {
		t.Fatal("projects/backend group not found")
	}

	// Name should be just "backend", not "projects/backend"
	if group.Name != "backend" {
		t.Errorf("Expected name 'backend', got '%s'", group.Name)
	}
}

func TestEnsureParentGroupsExist(t *testing.T) {
	tree := NewGroupTree([]*Instance{})

	// Call internal function to ensure parents exist
	tree.ensureParentGroupsExist("a/b/c")

	// All parent groups should exist
	if tree.Groups["a"] == nil {
		t.Error("Parent group 'a' should exist")
	}
	if tree.Groups["a/b"] == nil {
		t.Error("Parent group 'a/b' should exist")
	}
	// Note: "a/b/c" itself is NOT created by this function

	// Names should be correct
	if tree.Groups["a"].Name != "a" {
		t.Errorf("Expected name 'a', got '%s'", tree.Groups["a"].Name)
	}
	if tree.Groups["a/b"].Name != "b" {
		t.Errorf("Expected name 'b', got '%s'", tree.Groups["a/b"].Name)
	}
}

func TestEnsureParentGroupsExistRootLevel(t *testing.T) {
	tree := NewGroupTree([]*Instance{})

	// For root-level paths, no parents needed
	tree.ensureParentGroupsExist("root")

	// No groups should be created
	if len(tree.Groups) != 0 {
		t.Errorf("Expected 0 groups for root-level path, got %d", len(tree.Groups))
	}
}

func TestEnsureParentGroupsExistIdempotent(t *testing.T) {
	tree := NewGroupTree([]*Instance{})

	// Create parent group first
	tree.CreateGroup("existing")

	// Call ensureParentGroupsExist with a child path
	tree.ensureParentGroupsExist("existing/child")

	// Parent should still exist with original name (not overwritten)
	if tree.Groups["existing"] == nil {
		t.Error("Parent group 'existing' should still exist")
	}
	if tree.Groups["existing"].Name != "existing" {
		t.Errorf("Expected name 'existing', got '%s'", tree.Groups["existing"].Name)
	}
}

func TestNewGroupTreeAutoCreatesParents(t *testing.T) {
	// Session with deep hierarchical path - parents don't exist
	instances := []*Instance{
		{ID: "1", Title: "session-1", GroupPath: "projects/backend/api"},
	}

	tree := NewGroupTree(instances)

	// All groups should exist
	if tree.Groups["projects"] == nil {
		t.Error("Parent group 'projects' should be auto-created")
	}
	if tree.Groups["projects/backend"] == nil {
		t.Error("Parent group 'projects/backend' should be auto-created")
	}
	if tree.Groups["projects/backend/api"] == nil {
		t.Error("Group 'projects/backend/api' should exist")
	}

	// Names should be correct
	if tree.Groups["projects"].Name != "projects" {
		t.Errorf("Expected name 'projects', got '%s'", tree.Groups["projects"].Name)
	}
	if tree.Groups["projects/backend"].Name != "backend" {
		t.Errorf("Expected name 'backend', got '%s'", tree.Groups["projects/backend"].Name)
	}
	if tree.Groups["projects/backend/api"].Name != "api" {
		t.Errorf("Expected name 'api', got '%s'", tree.Groups["projects/backend/api"].Name)
	}
}

func TestAddSessionUpdatesDefaultPath(t *testing.T) {
	tree := NewGroupTree([]*Instance{})

	// Empty group should have no DefaultPath
	tree.AddSession(&Instance{
		ID:          "1",
		Title:       "first",
		GroupPath:   "dev",
		ProjectPath: "",
	})
	group := tree.Groups["dev"]
	if group == nil {
		t.Fatal("dev group not found after AddSession")
	}
	if group.DefaultPath != "" {
		t.Errorf("Expected empty DefaultPath for session with no ProjectPath, got %q", group.DefaultPath)
	}

	// After adding a session with a ProjectPath, DefaultPath should be set
	now := time.Now()
	tree.AddSession(&Instance{
		ID:             "2",
		Title:          "second",
		GroupPath:      "dev",
		ProjectPath:    "/home/user/project-a",
		LastAccessedAt: now,
	})
	if got := tree.DefaultPathForGroup("dev"); got != "/home/user/project-a" {
		t.Errorf("Expected default path '/home/user/project-a', got %q", got)
	}

	// After adding a more recently accessed session, DefaultPath should update
	tree.AddSession(&Instance{
		ID:             "3",
		Title:          "third",
		GroupPath:      "dev",
		ProjectPath:    "/home/user/project-b",
		LastAccessedAt: now.Add(time.Minute),
	})
	if got := tree.DefaultPathForGroup("dev"); got != "/home/user/project-b" {
		t.Errorf("Expected default path '/home/user/project-b', got %q", got)
	}

	// The stored field remains empty until explicitly configured.
	if group.DefaultPath != "" {
		t.Errorf("Expected stored DefaultPath to remain empty for derived defaults, got %q", group.DefaultPath)
	}
}

func TestMoveSessionUpOrder(t *testing.T) {
	instances := []*Instance{
		{ID: "a", Title: "first", GroupPath: "test"},
		{ID: "b", Title: "second", GroupPath: "test"},
		{ID: "c", Title: "third", GroupPath: "test"},
	}

	tree := NewGroupTree(instances)
	group := tree.Groups["test"]

	// Move second session up (swap with first)
	tree.MoveSessionUp(instances[1])

	// Verify slice order: b, a, c
	if group.Sessions[0].ID != "b" {
		t.Errorf("Expected 'b' at index 0, got '%s'", group.Sessions[0].ID)
	}
	if group.Sessions[1].ID != "a" {
		t.Errorf("Expected 'a' at index 1, got '%s'", group.Sessions[1].ID)
	}
	if group.Sessions[2].ID != "c" {
		t.Errorf("Expected 'c' at index 2, got '%s'", group.Sessions[2].ID)
	}

	// Verify Order field values are normalized
	for i, s := range group.Sessions {
		if s.Order != i {
			t.Errorf("Expected Order %d for session '%s', got %d", i, s.ID, s.Order)
		}
	}
}

func TestMoveSessionDownOrder(t *testing.T) {
	instances := []*Instance{
		{ID: "a", Title: "first", GroupPath: "test"},
		{ID: "b", Title: "second", GroupPath: "test"},
		{ID: "c", Title: "third", GroupPath: "test"},
	}

	tree := NewGroupTree(instances)
	group := tree.Groups["test"]

	// Move second session down (swap with third)
	tree.MoveSessionDown(instances[1])

	// Verify slice order: a, c, b
	if group.Sessions[0].ID != "a" {
		t.Errorf("Expected 'a' at index 0, got '%s'", group.Sessions[0].ID)
	}
	if group.Sessions[1].ID != "c" {
		t.Errorf("Expected 'c' at index 1, got '%s'", group.Sessions[1].ID)
	}
	if group.Sessions[2].ID != "b" {
		t.Errorf("Expected 'b' at index 2, got '%s'", group.Sessions[2].ID)
	}

	// Verify Order field values are normalized
	for i, s := range group.Sessions {
		if s.Order != i {
			t.Errorf("Expected Order %d for session '%s', got %d", i, s.ID, s.Order)
		}
	}
}

// childrenOf returns IDs of sub-sessions whose ParentSessionID matches parentID,
// in the order they appear in the group's flat session slice. Mirrors the
// rendering path's bucketing in groups.go::Items().
func childrenOf(group *Group, parentID string) []string {
	var ids []string
	for _, s := range group.Sessions {
		if s.ParentSessionID == parentID {
			ids = append(ids, s.ID)
		}
	}
	return ids
}

// TestMoveSessionUp_SubSessionSkipsNonSiblings documents the bug fix:
// when sub-sessions of different parents are interleaved in the flat
// group.Sessions slice, MoveSessionUp on a sub-session must swap with
// the previous SAME-parent sibling, not with the immediately-preceding
// non-sibling. Otherwise a single key press produces no visible change
// and the user has to press the reorder key multiple times.
func TestMoveSessionUp_SubSessionSkipsNonSiblings(t *testing.T) {
	instances := []*Instance{
		{ID: "p1", Title: "parent1", GroupPath: "test"},
		{ID: "s1a", Title: "child1a", GroupPath: "test", ParentSessionID: "p1"},
		{ID: "p2", Title: "parent2", GroupPath: "test"},
		{ID: "s1b", Title: "child1b", GroupPath: "test", ParentSessionID: "p1"},
	}
	tree := NewGroupTree(instances)
	group := tree.Groups["test"]

	var s1b *Instance
	for _, s := range group.Sessions {
		if s.ID == "s1b" {
			s1b = s
			break
		}
	}

	tree.MoveSessionUp(s1b)

	// After one move, the visual children-of-p1 order should be [s1b, s1a].
	got := childrenOf(group, "p1")
	want := []string{"s1b", "s1a"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("children of p1 after MoveSessionUp(s1b) = %v, want %v", got, want)
	}
}

func TestMoveSessionDown_SubSessionSkipsNonSiblings(t *testing.T) {
	instances := []*Instance{
		{ID: "p1", Title: "parent1", GroupPath: "test"},
		{ID: "s1a", Title: "child1a", GroupPath: "test", ParentSessionID: "p1"},
		{ID: "p2", Title: "parent2", GroupPath: "test"},
		{ID: "s1b", Title: "child1b", GroupPath: "test", ParentSessionID: "p1"},
	}
	tree := NewGroupTree(instances)
	group := tree.Groups["test"]

	var s1a *Instance
	for _, s := range group.Sessions {
		if s.ID == "s1a" {
			s1a = s
			break
		}
	}

	tree.MoveSessionDown(s1a)

	got := childrenOf(group, "p1")
	want := []string{"s1b", "s1a"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("children of p1 after MoveSessionDown(s1a) = %v, want %v", got, want)
	}
}

// TestMoveSessionUp_FirstChildPromotes: K on the first child of a parent
// promotes the child to top-level, inserted in the slice immediately before
// the parent (so the renderer places it as a top-level peer just above the
// parent block). The remaining children stay under the parent.
func TestMoveSessionUp_FirstChildPromotes(t *testing.T) {
	instances := []*Instance{
		{ID: "p1", Title: "parent1", GroupPath: "test"},
		{ID: "s1a", Title: "child1a", GroupPath: "test", ParentSessionID: "p1"},
		{ID: "s1b", Title: "child1b", GroupPath: "test", ParentSessionID: "p1"},
	}
	tree := NewGroupTree(instances)
	group := tree.Groups["test"]

	var s1a *Instance
	for _, s := range group.Sessions {
		if s.ID == "s1a" {
			s1a = s
			break
		}
	}

	tree.MoveSessionUp(s1a)

	if s1a.ParentSessionID != "" {
		t.Errorf("s1a.ParentSessionID after promote = %q, want empty", s1a.ParentSessionID)
	}
	gotTop := childrenOf(group, "")
	wantTop := []string{"s1a", "p1"}
	if len(gotTop) != len(wantTop) || gotTop[0] != wantTop[0] || gotTop[1] != wantTop[1] {
		t.Errorf("top-level order after promote = %v, want %v", gotTop, wantTop)
	}
	gotKids := childrenOf(group, "p1")
	wantKids := []string{"s1b"}
	if len(gotKids) != len(wantKids) || gotKids[0] != wantKids[0] {
		t.Errorf("p1's children after promote = %v, want %v", gotKids, wantKids)
	}
}

// TestMoveSessionDown_LastChildPromotes: J on the last child of a parent
// promotes the child to top-level. The slice position stays where the child
// already was (right after the parent's other children), so the renderer
// shows it as a top-level peer immediately after the parent block.
func TestMoveSessionDown_LastChildPromotes(t *testing.T) {
	instances := []*Instance{
		{ID: "p1", Title: "parent1", GroupPath: "test"},
		{ID: "s1a", Title: "child1a", GroupPath: "test", ParentSessionID: "p1"},
		{ID: "s1b", Title: "child1b", GroupPath: "test", ParentSessionID: "p1"},
		{ID: "p2", Title: "parent2", GroupPath: "test"},
	}
	tree := NewGroupTree(instances)
	group := tree.Groups["test"]

	var s1b *Instance
	for _, s := range group.Sessions {
		if s.ID == "s1b" {
			s1b = s
			break
		}
	}

	tree.MoveSessionDown(s1b)

	if s1b.ParentSessionID != "" {
		t.Errorf("s1b.ParentSessionID after promote = %q, want empty", s1b.ParentSessionID)
	}
	gotTop := childrenOf(group, "")
	wantTop := []string{"p1", "s1b", "p2"}
	if len(gotTop) != len(wantTop) {
		t.Fatalf("top-level after promote = %v, want %v", gotTop, wantTop)
	}
	for i := range wantTop {
		if gotTop[i] != wantTop[i] {
			t.Errorf("top-level[%d] = %q, want %q", i, gotTop[i], wantTop[i])
		}
	}
	gotKids := childrenOf(group, "p1")
	wantKids := []string{"s1a"}
	if len(gotKids) != len(wantKids) || gotKids[0] != wantKids[0] {
		t.Errorf("p1's children after promote = %v, want %v", gotKids, wantKids)
	}
}

// TestMoveSessionDown_OnlyChildPromotes: J on the only child of a parent
// promotes the child to top-level; parent ends up with no children.
func TestMoveSessionDown_OnlyChildPromotes(t *testing.T) {
	instances := []*Instance{
		{ID: "p1", Title: "parent1", GroupPath: "test"},
		{ID: "s1", Title: "child1", GroupPath: "test", ParentSessionID: "p1"},
		{ID: "p2", Title: "parent2", GroupPath: "test"},
	}
	tree := NewGroupTree(instances)
	group := tree.Groups["test"]

	var s1 *Instance
	for _, s := range group.Sessions {
		if s.ID == "s1" {
			s1 = s
			break
		}
	}

	tree.MoveSessionDown(s1)

	if s1.ParentSessionID != "" {
		t.Errorf("s1.ParentSessionID after promote = %q, want empty", s1.ParentSessionID)
	}
	gotTop := childrenOf(group, "")
	wantTop := []string{"p1", "s1", "p2"}
	if len(gotTop) != len(wantTop) {
		t.Fatalf("top-level after promote = %v, want %v", gotTop, wantTop)
	}
	for i := range wantTop {
		if gotTop[i] != wantTop[i] {
			t.Errorf("top-level[%d] = %q, want %q", i, gotTop[i], wantTop[i])
		}
	}
	if len(childrenOf(group, "p1")) != 0 {
		t.Errorf("p1's children after promoting only child = %v, want []", childrenOf(group, "p1"))
	}
}

// TestMoveSessionUp_OnlyChildPromotes: K on the only child of a parent
// promotes it before the parent. Parent ends up with no children.
func TestMoveSessionUp_OnlyChildPromotes(t *testing.T) {
	instances := []*Instance{
		{ID: "p0", Title: "parent0", GroupPath: "test"},
		{ID: "p1", Title: "parent1", GroupPath: "test"},
		{ID: "s1", Title: "child1", GroupPath: "test", ParentSessionID: "p1"},
	}
	tree := NewGroupTree(instances)
	group := tree.Groups["test"]

	var s1 *Instance
	for _, s := range group.Sessions {
		if s.ID == "s1" {
			s1 = s
			break
		}
	}

	tree.MoveSessionUp(s1)

	if s1.ParentSessionID != "" {
		t.Errorf("s1.ParentSessionID after promote = %q, want empty", s1.ParentSessionID)
	}
	gotTop := childrenOf(group, "")
	wantTop := []string{"p0", "s1", "p1"}
	if len(gotTop) != len(wantTop) {
		t.Fatalf("top-level after promote = %v, want %v", gotTop, wantTop)
	}
	for i := range wantTop {
		if gotTop[i] != wantTop[i] {
			t.Errorf("top-level[%d] = %q, want %q", i, gotTop[i], wantTop[i])
		}
	}
	if len(childrenOf(group, "p1")) != 0 {
		t.Errorf("p1's children after promoting only child = %v, want []", childrenOf(group, "p1"))
	}
}

// TestMoveSessionUp_TopLevelAtFirstNoOp: K on the first top-level session
// in a group is a no-op. Cross-group moves stay on the M shortcut.
func TestMoveSessionUp_TopLevelAtFirstNoOp(t *testing.T) {
	instances := []*Instance{
		{ID: "p1", Title: "parent1", GroupPath: "test"},
		{ID: "s1", Title: "child1", GroupPath: "test", ParentSessionID: "p1"},
		{ID: "p2", Title: "parent2", GroupPath: "test"},
	}
	tree := NewGroupTree(instances)
	group := tree.Groups["test"]

	var p1 *Instance
	for _, s := range group.Sessions {
		if s.ID == "p1" {
			p1 = s
			break
		}
	}

	before := []string{}
	for _, s := range group.Sessions {
		before = append(before, s.ID)
	}
	tree.MoveSessionUp(p1)
	after := []string{}
	for _, s := range group.Sessions {
		after = append(after, s.ID)
	}
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("first top-level should not move: %v -> %v", before, after)
			break
		}
	}
}

// TestMoveSessionDown_TopLevelAtLastNoOp: J on the last top-level session
// in a group is a no-op. Cross-group moves stay on the M shortcut.
func TestMoveSessionDown_TopLevelAtLastNoOp(t *testing.T) {
	instances := []*Instance{
		{ID: "p1", Title: "parent1", GroupPath: "test"},
		{ID: "p2", Title: "parent2", GroupPath: "test"},
		{ID: "s2", Title: "child2", GroupPath: "test", ParentSessionID: "p2"},
	}
	tree := NewGroupTree(instances)
	group := tree.Groups["test"]

	var p2 *Instance
	for _, s := range group.Sessions {
		if s.ID == "p2" {
			p2 = s
			break
		}
	}

	before := []string{}
	for _, s := range group.Sessions {
		before = append(before, s.ID)
	}
	tree.MoveSessionDown(p2)
	after := []string{}
	for _, s := range group.Sessions {
		after = append(after, s.ID)
	}
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("last top-level should not move: %v -> %v", before, after)
			break
		}
	}
}

// TestMoveSessionUp_TopLevelSkipsSubSessions: a top-level session moved up
// should swap with the previous TOP-LEVEL session, skipping any sub-sessions
// belonging to another parent that happen to be between them in the slice.
func TestMoveSessionUp_TopLevelSkipsSubSessions(t *testing.T) {
	instances := []*Instance{
		{ID: "p1", Title: "parent1", GroupPath: "test"},
		{ID: "s1a", Title: "child1a", GroupPath: "test", ParentSessionID: "p1"},
		{ID: "p2", Title: "parent2", GroupPath: "test"},
	}
	tree := NewGroupTree(instances)
	group := tree.Groups["test"]

	var p2 *Instance
	for _, s := range group.Sessions {
		if s.ID == "p2" {
			p2 = s
			break
		}
	}

	tree.MoveSessionUp(p2)

	// Top-level peers (sessions with empty ParentSessionID) order should
	// now be [p2, p1].
	got := childrenOf(group, "")
	want := []string{"p2", "p1"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("top-level order after MoveSessionUp(p2) = %v, want %v", got, want)
	}
}

func TestSessionOrderPersistence(t *testing.T) {
	// Simulate sessions with Order values (as if saved after reorder)
	instances := []*Instance{
		{ID: "a", Title: "first", GroupPath: "test", Order: 2},
		{ID: "b", Title: "second", GroupPath: "test", Order: 0},
		{ID: "c", Title: "third", GroupPath: "test", Order: 1},
	}

	storedGroups := []*GroupData{
		{Name: "test", Path: "test", Expanded: true, Order: 0},
	}

	tree := NewGroupTreeWithGroups(instances, storedGroups)
	group := tree.Groups["test"]

	// Sessions should be sorted by Order: b(0), c(1), a(2)
	if group.Sessions[0].ID != "b" {
		t.Errorf("Expected 'b' at index 0 (Order 0), got '%s' (Order %d)", group.Sessions[0].ID, group.Sessions[0].Order)
	}
	if group.Sessions[1].ID != "c" {
		t.Errorf("Expected 'c' at index 1 (Order 1), got '%s' (Order %d)", group.Sessions[1].ID, group.Sessions[1].Order)
	}
	if group.Sessions[2].ID != "a" {
		t.Errorf("Expected 'a' at index 2 (Order 2), got '%s' (Order %d)", group.Sessions[2].ID, group.Sessions[2].Order)
	}
}

func TestSessionOrderMigration(t *testing.T) {
	// Simulate legacy sessions with no Order field (all zero)
	// SliceStable should preserve original JSON array order
	instances := []*Instance{
		{ID: "x", Title: "first", GroupPath: "test", Order: 0},
		{ID: "y", Title: "second", GroupPath: "test", Order: 0},
		{ID: "z", Title: "third", GroupPath: "test", Order: 0},
	}

	storedGroups := []*GroupData{
		{Name: "test", Path: "test", Expanded: true, Order: 0},
	}

	tree := NewGroupTreeWithGroups(instances, storedGroups)
	group := tree.Groups["test"]

	// With all Order==0, SliceStable preserves original order: x, y, z
	if group.Sessions[0].ID != "x" {
		t.Errorf("Expected 'x' at index 0 (stable sort), got '%s'", group.Sessions[0].ID)
	}
	if group.Sessions[1].ID != "y" {
		t.Errorf("Expected 'y' at index 1 (stable sort), got '%s'", group.Sessions[1].ID)
	}
	if group.Sessions[2].ID != "z" {
		t.Errorf("Expected 'z' at index 2 (stable sort), got '%s'", group.Sessions[2].ID)
	}
}

func TestSyncWithInstancesUpdatesDefaultPath(t *testing.T) {
	now := time.Now()
	instances := []*Instance{
		{
			ID:             "1",
			Title:          "older",
			GroupPath:      "work",
			ProjectPath:    "/old/path",
			LastAccessedAt: now,
		},
		{
			ID:             "2",
			Title:          "newer",
			GroupPath:      "work",
			ProjectPath:    "/new/path",
			LastAccessedAt: now.Add(time.Hour),
		},
	}

	tree := NewGroupTree([]*Instance{})
	tree.SyncWithInstances(instances)

	group := tree.Groups["work"]
	if group == nil {
		t.Fatal("work group not found after SyncWithInstances")
	}
	if got := tree.DefaultPathForGroup("work"); got != "/new/path" {
		t.Errorf("Expected default path '/new/path' after sync, got %q", got)
	}
	if group.DefaultPath != "" {
		t.Errorf("Expected stored DefaultPath to remain empty for derived defaults, got %q", group.DefaultPath)
	}
}

func TestSubgroupAppearsAfterParent(t *testing.T) {
	tree := NewGroupTree([]*Instance{})

	// Create multiple root groups
	tree.CreateGroup("Alpha")
	tree.CreateGroup("Beta")
	tree.CreateGroup("Gamma")

	// Create subgroup under Beta
	child := tree.CreateSubgroup("Beta", "Child")

	// Verify path is correct
	if child.Path != "Beta/Child" {
		t.Errorf("Expected path 'Beta/Child', got '%s'", child.Path)
	}

	// Verify parent-child relationship in GroupList ordering
	var betaIdx, childIdx int = -1, -1
	for i, g := range tree.GroupList {
		if g.Path == "Beta" {
			betaIdx = i
		}
		if g.Path == "Beta/Child" {
			childIdx = i
		}
	}

	// Child should come after parent in GroupList
	if childIdx <= betaIdx {
		t.Errorf("Subgroup should appear after parent in GroupList. Parent at %d, child at %d",
			betaIdx, childIdx)
	}
}

func TestSortingTransitivity(t *testing.T) {
	// Reproduce the exact scenario that caused the bug:
	// When alphabetical order differs from creation order, deep nesting
	// could cause children to appear before their parents
	tree := NewGroupTree([]*Instance{})

	// Create "My Sessions" first (Order=0), then "Beta" (Order=1)
	// Alphabetically: "Beta" < "My-Sessions", but by Order: My-Sessions < Beta
	tree.CreateGroup("My Sessions")
	tree.CreateGroup("Beta")
	tree.CreateSubgroup("Beta", "Tasks")
	tree.CreateSubgroup("Beta/Tasks", "Urgent")

	// Verify Beta comes before its descendants
	var betaIdx, tasksIdx, urgentIdx int = -1, -1, -1
	for i, g := range tree.GroupList {
		switch g.Path {
		case "Beta":
			betaIdx = i
		case "Beta/Tasks":
			tasksIdx = i
		case "Beta/Tasks/Urgent":
			urgentIdx = i
		}
	}

	if betaIdx == -1 || tasksIdx == -1 || urgentIdx == -1 {
		t.Fatal("Expected groups not found in GroupList")
	}

	// Parent chain should be in order: beta < tasks < urgent
	if !(betaIdx < tasksIdx && tasksIdx < urgentIdx) {
		t.Errorf("Parent chain out of order: beta=%d, tasks=%d, urgent=%d",
			betaIdx, tasksIdx, urgentIdx)
	}
}

func TestBranchOrderingByOrder(t *testing.T) {
	tree := NewGroupTree([]*Instance{})

	// Create groups where alphabetical order differs from Order
	tree.CreateGroup("Zebra") // Created first, Order=0
	tree.CreateGroup("Alpha") // Created second, Order=1

	// Create subgroups
	tree.CreateSubgroup("Zebra", "Child")
	tree.CreateSubgroup("Alpha", "Child")

	// Zebra branch should come before Alpha branch (by Order, not alphabetically)
	var zebraIdx, alphaIdx int = -1, -1
	for i, g := range tree.GroupList {
		if g.Path == "Zebra" {
			zebraIdx = i
		}
		if g.Path == "Alpha" {
			alphaIdx = i
		}
	}

	if zebraIdx > alphaIdx {
		t.Errorf("Zebra (Order=0) should come before Alpha (Order=1). Zebra=%d, Alpha=%d",
			zebraIdx, alphaIdx)
	}
}

func TestFlattenIncludesWindowType(t *testing.T) {
	instances := []*Instance{
		{ID: "1", Title: "session-1", GroupPath: "project-a"},
	}
	tree := NewGroupTree(instances)
	tree.ExpandGroup("project-a")

	items := tree.Flatten()

	// Without windows, just group + session
	sessionCount := 0
	windowCount := 0
	for _, item := range items {
		if item.Type == ItemTypeSession {
			sessionCount++
		}
		if item.Type == ItemTypeWindow {
			windowCount++
		}
	}
	assert.Equal(t, 1, sessionCount)
	assert.Equal(t, 0, windowCount, "no windows injected at Flatten level")
}

// TestPromoteSession_SubSessionBecomesTopLevel: explicit Shift+Left promote
// converts a sub-session into a top-level peer. Slice position is preserved
// so the renderer places it after the parent's children block.
func TestPromoteSession_SubSessionBecomesTopLevel(t *testing.T) {
	instances := []*Instance{
		{ID: "p1", Title: "parent1", GroupPath: "test"},
		{ID: "s1a", Title: "child1a", GroupPath: "test", ParentSessionID: "p1"},
		{ID: "s1b", Title: "child1b", GroupPath: "test", ParentSessionID: "p1"},
		{ID: "p2", Title: "parent2", GroupPath: "test"},
	}
	tree := NewGroupTree(instances)
	group := tree.Groups["test"]

	var s1a *Instance
	for _, s := range group.Sessions {
		if s.ID == "s1a" {
			s1a = s
			break
		}
	}

	tree.PromoteSession(s1a)

	if s1a.ParentSessionID != "" {
		t.Errorf("s1a.ParentSessionID after promote = %q, want empty", s1a.ParentSessionID)
	}
	gotKids := childrenOf(group, "p1")
	wantKids := []string{"s1b"}
	if len(gotKids) != len(wantKids) || gotKids[0] != wantKids[0] {
		t.Errorf("p1's children after promoting s1a = %v, want %v", gotKids, wantKids)
	}
	gotTop := childrenOf(group, "")
	wantTop := []string{"p1", "s1a", "p2"}
	if len(gotTop) != len(wantTop) {
		t.Fatalf("top-level after promote = %v, want %v", gotTop, wantTop)
	}
	for i := range wantTop {
		if gotTop[i] != wantTop[i] {
			t.Errorf("top-level[%d] = %q, want %q", i, gotTop[i], wantTop[i])
		}
	}
}
// TestPromoteSession_TopLevelNoOp: PromoteSession on an already-top-level
// session is a no-op.
func TestPromoteSession_TopLevelNoOp(t *testing.T) {
	instances := []*Instance{
		{ID: "p1", Title: "parent1", GroupPath: "test"},
		{ID: "p2", Title: "parent2", GroupPath: "test"},
	}
	tree := NewGroupTree(instances)
	group := tree.Groups["test"]

	var p2 *Instance
	for _, s := range group.Sessions {
		if s.ID == "p2" {
			p2 = s
			break
		}
	}

	before := []string{}
	for _, s := range group.Sessions {
		before = append(before, s.ID)
	}
	tree.PromoteSession(p2)
	after := []string{}
	for _, s := range group.Sessions {
		after = append(after, s.ID)
	}
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("PromoteSession on top-level should be no-op; got %v -> %v", before, after)
			break
		}
	}
}

// TestDemoteSession_TopLevelBecomesLastChild: explicit Shift+Right demote
// makes the cursor's session a sub-session of the previous top-level peer,
// inserted as that peer's last child.
func TestDemoteSession_TopLevelBecomesLastChild(t *testing.T) {
	instances := []*Instance{
		{ID: "p1", Title: "parent1", GroupPath: "test"},
		{ID: "s1a", Title: "child1a", GroupPath: "test", ParentSessionID: "p1"},
		{ID: "p2", Title: "parent2", GroupPath: "test"},
	}
	tree := NewGroupTree(instances)
	group := tree.Groups["test"]

	var p2 *Instance
	for _, s := range group.Sessions {
		if s.ID == "p2" {
			p2 = s
			break
		}
	}

	tree.DemoteSession(p2)

	if p2.ParentSessionID != "p1" {
		t.Errorf("p2.ParentSessionID after demote = %q, want %q", p2.ParentSessionID, "p1")
	}
	gotKids := childrenOf(group, "p1")
	wantKids := []string{"s1a", "p2"}
	if len(gotKids) != len(wantKids) {
		t.Fatalf("p1's children after demoting p2 = %v, want %v", gotKids, wantKids)
	}
	for i := range wantKids {
		if gotKids[i] != wantKids[i] {
			t.Errorf("p1's children[%d] = %q, want %q", i, gotKids[i], wantKids[i])
		}
	}
	gotTop := childrenOf(group, "")
	wantTop := []string{"p1"}
	if len(gotTop) != len(wantTop) || gotTop[0] != wantTop[0] {
		t.Errorf("top-level after demote = %v, want %v", gotTop, wantTop)
	}
}
// TestDemoteSession_FirstTopLevelNoOp: DemoteSession on the first top-level
// in a group is a no-op (no previous peer to nest under). Cross-group moves
// stay on the M shortcut.
func TestDemoteSession_FirstTopLevelNoOp(t *testing.T) {
	instances := []*Instance{
		{ID: "p1", Title: "parent1", GroupPath: "test"},
		{ID: "p2", Title: "parent2", GroupPath: "test"},
	}
	tree := NewGroupTree(instances)
	group := tree.Groups["test"]

	var p1 *Instance
	for _, s := range group.Sessions {
		if s.ID == "p1" {
			p1 = s
			break
		}
	}

	tree.DemoteSession(p1)

	if p1.ParentSessionID != "" {
		t.Errorf("p1 should remain top-level; got ParentSessionID = %q", p1.ParentSessionID)
	}
}
// TestDemoteSession_SubSessionNoOp: demote on an already-sub-session is a
// no-op (single-level nesting only).
func TestDemoteSession_SubSessionNoOp(t *testing.T) {
	instances := []*Instance{
		{ID: "p1", Title: "parent1", GroupPath: "test"},
		{ID: "s1", Title: "child1", GroupPath: "test", ParentSessionID: "p1"},
		{ID: "p2", Title: "parent2", GroupPath: "test"},
	}
	tree := NewGroupTree(instances)
	group := tree.Groups["test"]

	var s1 *Instance
	for _, s := range group.Sessions {
		if s.ID == "s1" {
			s1 = s
			break
		}
	}

	tree.DemoteSession(s1)

	if s1.ParentSessionID != "p1" {
		t.Errorf("sub-session should not change parent on demote; got %q", s1.ParentSessionID)
	}
}
// TestDemoteSession_WithChildrenNoOp: a top-level session that already has
// children of its own cannot be demoted (single-level nesting invariant,
// matches `session set-parent` validation).
func TestDemoteSession_WithChildrenNoOp(t *testing.T) {
	instances := []*Instance{
		{ID: "p1", Title: "parent1", GroupPath: "test"},
		{ID: "p2", Title: "parent2", GroupPath: "test"},
		{ID: "s2", Title: "child2", GroupPath: "test", ParentSessionID: "p2"},
	}
	tree := NewGroupTree(instances)
	group := tree.Groups["test"]

	var p2 *Instance
	for _, s := range group.Sessions {
		if s.ID == "p2" {
			p2 = s
			break
		}
	}

	tree.DemoteSession(p2)

	if p2.ParentSessionID != "" {
		t.Errorf("p2 with children should not be demoted; got ParentSessionID = %q", p2.ParentSessionID)
	}
	gotKids := childrenOf(group, "p2")
	wantKids := []string{"s2"}
	if len(gotKids) != len(wantKids) || gotKids[0] != wantKids[0] {
		t.Errorf("p2's children should be unchanged; got %v, want %v", gotKids, wantKids)
	}
}
