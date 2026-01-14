package session

import (
	"testing"
	"time"
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
	if group.Path != "my-project" {
		t.Errorf("Expected path 'my-project', got '%s'", group.Path)
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
	child := tree.CreateSubgroup("parent", "Child")
	if child == nil {
		t.Fatal("CreateSubgroup returned nil")
	}

	if child.Name != "Child" {
		t.Errorf("Expected name 'Child', got '%s'", child.Name)
	}
	if child.Path != "parent/child" {
		t.Errorf("Expected path 'parent/child', got '%s'", child.Path)
	}
	if tree.GroupCount() != 2 {
		t.Errorf("Expected 2 groups, got %d", tree.GroupCount())
	}
}

func TestCreateNestedSubgroups(t *testing.T) {
	tree := NewGroupTree([]*Instance{})

	// Create hierarchy: grandparent -> parent -> child
	tree.CreateGroup("Grandparent")
	tree.CreateSubgroup("grandparent", "Parent")
	tree.CreateSubgroup("grandparent/parent", "Child")

	if tree.GroupCount() != 3 {
		t.Errorf("Expected 3 groups, got %d", tree.GroupCount())
	}

	child := tree.Groups["grandparent/parent/child"]
	if child == nil {
		t.Fatal("Nested child group not found")
	}
	if child.Path != "grandparent/parent/child" {
		t.Errorf("Expected path 'grandparent/parent/child', got '%s'", child.Path)
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
	tree.CreateSubgroup("parent", "Child")

	// Add sessions
	tree.Groups["parent"].Sessions = []*Instance{{ID: "1", GroupPath: "parent"}}
	tree.Groups["parent/child"].Sessions = []*Instance{{ID: "2", GroupPath: "parent/child"}}

	// Expand all first
	tree.ExpandGroup("parent")
	tree.ExpandGroup("parent/child")

	items := tree.Flatten()
	// parent(group) + session + child(group) + session = 4
	if len(items) != 4 {
		t.Errorf("Expected 4 items when expanded, got %d", len(items))
	}

	// Collapse parent - should hide child group and all sessions
	tree.CollapseGroup("parent")
	items = tree.Flatten()

	// Only parent group visible
	if len(items) != 1 {
		t.Errorf("Expected 1 item when parent collapsed, got %d", len(items))
	}
}

func TestToggleGroup(t *testing.T) {
	tree := NewGroupTree([]*Instance{})
	tree.CreateGroup("Test")

	// Initially expanded
	if !tree.Groups["test"].Expanded {
		t.Error("Group should be expanded initially")
	}

	// Toggle to collapse
	tree.ToggleGroup("test")
	if tree.Groups["test"].Expanded {
		t.Error("Group should be collapsed after toggle")
	}

	// Toggle to expand
	tree.ToggleGroup("test")
	if !tree.Groups["test"].Expanded {
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
	newGroup := tree.Groups["new-name"]
	if newGroup == nil {
		t.Fatal("New group not found")
	}

	if newGroup.Name != "New Name" {
		t.Errorf("Expected name 'New Name', got '%s'", newGroup.Name)
	}

	// Session should be updated
	if instances[0].GroupPath != "new-name" {
		t.Errorf("Session GroupPath not updated, got '%s'", instances[0].GroupPath)
	}
}

func TestRenameGroupWithSubgroups(t *testing.T) {
	tree := NewGroupTree([]*Instance{})

	// Create hierarchy
	tree.CreateGroup("Parent")
	tree.CreateSubgroup("parent", "Child")
	tree.CreateSubgroup("parent/child", "Grandchild")

	// Add sessions to each
	tree.Groups["parent"].Sessions = []*Instance{{ID: "1", GroupPath: "parent"}}
	tree.Groups["parent/child"].Sessions = []*Instance{{ID: "2", GroupPath: "parent/child"}}
	tree.Groups["parent/child/grandchild"].Sessions = []*Instance{{ID: "3", GroupPath: "parent/child/grandchild"}}

	// Rename parent
	tree.RenameGroup("parent", "NewParent")

	// Verify old paths don't exist
	if tree.Groups["parent"] != nil {
		t.Error("Old parent path should not exist")
	}
	if tree.Groups["parent/child"] != nil {
		t.Error("Old child path should not exist")
	}
	if tree.Groups["parent/child/grandchild"] != nil {
		t.Error("Old grandchild path should not exist")
	}

	// Verify new paths exist
	if tree.Groups["newparent"] == nil {
		t.Error("New parent path should exist")
	}
	if tree.Groups["newparent/child"] == nil {
		t.Error("New child path should exist")
	}
	if tree.Groups["newparent/child/grandchild"] == nil {
		t.Error("New grandchild path should exist")
	}

	// Verify session GroupPaths updated
	if tree.Groups["newparent"].Sessions[0].GroupPath != "newparent" {
		t.Error("Parent session GroupPath not updated")
	}
	if tree.Groups["newparent/child"].Sessions[0].GroupPath != "newparent/child" {
		t.Error("Child session GroupPath not updated")
	}
	if tree.Groups["newparent/child/grandchild"].Sessions[0].GroupPath != "newparent/child/grandchild" {
		t.Error("Grandchild session GroupPath not updated")
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
	tree.CreateSubgroup("parent", "Child")

	// Note: DeleteGroup creates the default group if it doesn't exist
	// when it moves sessions there

	// Add sessions
	tree.Groups["parent"].Sessions = []*Instance{{ID: "1", GroupPath: "parent"}}
	tree.Groups["parent/child"].Sessions = []*Instance{{ID: "2", GroupPath: "parent/child"}}

	// Delete parent - should cascade to child
	movedSessions := tree.DeleteGroup("parent")

	// Both groups should be removed
	if tree.Groups["parent"] != nil {
		t.Error("Parent group should be deleted")
	}
	if tree.Groups["parent/child"] != nil {
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

func TestMoveGroupUpDownSiblings(t *testing.T) {
	tree := NewGroupTree([]*Instance{})

	// Create sibling groups
	tree.CreateGroup("Alpha")
	tree.CreateGroup("Beta")
	tree.CreateGroup("Gamma")

	// Initial order: alpha, beta, gamma
	if tree.GroupList[0].Path != "alpha" {
		t.Errorf("Expected alpha first, got %s", tree.GroupList[0].Path)
	}

	// Move beta up - should swap with alpha
	tree.MoveGroupUp("beta")
	if tree.GroupList[0].Path != "beta" {
		t.Errorf("Expected beta first after move up, got %s", tree.GroupList[0].Path)
	}

	// Move beta down - should swap with alpha
	tree.MoveGroupDown("beta")
	if tree.GroupList[1].Path != "beta" {
		t.Errorf("Expected beta second after move down, got %s", tree.GroupList[1].Path)
	}
}

func TestMoveGroupNotAcrossLevels(t *testing.T) {
	tree := NewGroupTree([]*Instance{})

	// Create parent and child
	tree.CreateGroup("Parent")
	tree.CreateSubgroup("parent", "Child")

	// Try to move child up - should not swap with parent (different levels)
	initialOrder := make([]string, len(tree.GroupList))
	for i, g := range tree.GroupList {
		initialOrder[i] = g.Path
	}

	tree.MoveGroupUp("parent/child")

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
	tree.CreateSubgroup("alpha", "Child")

	paths := tree.GetGroupPaths()

	if len(paths) != 2 {
		t.Errorf("Expected 2 paths, got %d", len(paths))
	}

	// Check paths contain expected values
	foundAlpha := false
	foundChild := false
	for _, p := range paths {
		if p == "alpha" {
			foundAlpha = true
		}
		if p == "alpha/child" {
			foundChild = true
		}
	}

	if !foundAlpha || !foundChild {
		t.Error("Expected both alpha and alpha/child paths")
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

func TestNewGroupTreeWithGroups_PreservesDefaultPathForEmptyGroup(t *testing.T) {
	storedGroups := []*GroupData{
		{Name: "empty-group", Path: "empty-group", DefaultPath: "/tmp/empty", Expanded: false, Order: 0},
	}

	tree := NewGroupTreeWithGroups(nil, storedGroups)

	group := tree.Groups["empty-group"]
	if group == nil {
		t.Fatal("empty-group should exist")
	}
	if group.DefaultPath != "/tmp/empty" {
		t.Errorf("DefaultPath = %q, want %q", group.DefaultPath, "/tmp/empty")
	}
}

func TestUpdateDefaultPathForGroup_MostRecent(t *testing.T) {
	now := time.Now()
	instances := []*Instance{
		{
			ID:             "1",
			Title:          "old",
			GroupPath:      "work",
			ProjectPath:    "/tmp/old",
			CreatedAt:      now.Add(-2 * time.Hour),
			LastAccessedAt: now.Add(-90 * time.Minute),
		},
		{
			ID:             "2",
			Title:          "new",
			GroupPath:      "work",
			ProjectPath:    "/tmp/new",
			CreatedAt:      now.Add(-1 * time.Hour),
			LastAccessedAt: now.Add(-30 * time.Minute),
		},
	}

	tree := NewGroupTree(instances)
	group := tree.Groups["work"]
	if group == nil {
		t.Fatal("work group should exist")
	}
	if group.DefaultPath != "/tmp/new" {
		t.Errorf("DefaultPath = %q, want %q", group.DefaultPath, "/tmp/new")
	}
}

func TestUpdateDefaultPathForGroup_WorktreePrefersRepoRoot(t *testing.T) {
	now := time.Now()
	instances := []*Instance{
		{
			ID:               "1",
			Title:            "worktree",
			GroupPath:        "work",
			ProjectPath:      "/tmp/repo/.worktrees/feature",
			WorktreePath:     "/tmp/repo/.worktrees/feature",
			WorktreeRepoRoot: "/tmp/repo",
			CreatedAt:        now.Add(-1 * time.Hour),
			LastAccessedAt:   now.Add(-30 * time.Minute),
		},
	}

	tree := NewGroupTree(instances)
	group := tree.Groups["work"]
	if group == nil {
		t.Fatal("work group should exist")
	}
	if group.DefaultPath != "/tmp/repo" {
		t.Errorf("DefaultPath = %q, want %q", group.DefaultPath, "/tmp/repo")
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
