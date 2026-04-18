package session

import "testing"

// Tests for issue #447 — MoveGroupTo reparents a group under a new parent
// (or to root when destParentPath == "").

// TestMoveGroupTo_ToRoot moves a nested group to root and verifies paths.
func TestMoveGroupTo_ToRoot(t *testing.T) {
	tree := NewGroupTree([]*Instance{})
	tree.CreateGroup("Parent")
	tree.CreateSubgroup("Parent", "Child")

	sess := &Instance{ID: "s1", GroupPath: "Parent/Child"}
	tree.Groups["Parent/Child"].Sessions = []*Instance{sess}

	if err := tree.MoveGroupTo("Parent/Child", ""); err != nil {
		t.Fatalf("MoveGroupTo returned error: %v", err)
	}

	if tree.Groups["Parent/Child"] != nil {
		t.Error("old path Parent/Child should no longer exist")
	}
	child := tree.Groups["Child"]
	if child == nil {
		t.Fatal("expected group at new path 'Child'")
	}
	if child.Path != "Child" {
		t.Errorf("group.Path = %q, want %q", child.Path, "Child")
	}
	if sess.GroupPath != "Child" {
		t.Errorf("session GroupPath = %q, want %q", sess.GroupPath, "Child")
	}
	if tree.Groups["Parent"] == nil {
		t.Error("parent group Parent should still exist")
	}
}

// TestMoveGroupTo_ToOtherParent moves one root group to become a subgroup of another.
func TestMoveGroupTo_ToOtherParent(t *testing.T) {
	tree := NewGroupTree([]*Instance{})
	tree.CreateGroup("personal")
	tree.CreateSubgroup("personal", "project1")
	tree.CreateGroup("work")

	sess := &Instance{ID: "s1", GroupPath: "personal/project1"}
	tree.Groups["personal/project1"].Sessions = []*Instance{sess}

	if err := tree.MoveGroupTo("personal/project1", "work"); err != nil {
		t.Fatalf("MoveGroupTo returned error: %v", err)
	}

	if tree.Groups["personal/project1"] != nil {
		t.Error("old path personal/project1 should not exist")
	}
	moved := tree.Groups["work/project1"]
	if moved == nil {
		t.Fatal("expected group at new path 'work/project1'")
	}
	if moved.Path != "work/project1" {
		t.Errorf("group.Path = %q, want %q", moved.Path, "work/project1")
	}
	if sess.GroupPath != "work/project1" {
		t.Errorf("session GroupPath = %q, want %q", sess.GroupPath, "work/project1")
	}
	if tree.Groups["personal"] == nil {
		t.Error("personal group should still exist (empty, but present)")
	}
}

// TestMoveGroupTo_WithSubgroups verifies that subgroups and their sessions
// follow the moved group and all get their paths rewritten.
func TestMoveGroupTo_WithSubgroups(t *testing.T) {
	tree := NewGroupTree([]*Instance{})
	tree.CreateGroup("personal")
	tree.CreateSubgroup("personal", "project1")
	tree.CreateSubgroup("personal/project1", "backend")
	tree.CreateGroup("work")

	sA := &Instance{ID: "a", GroupPath: "personal/project1"}
	sB := &Instance{ID: "b", GroupPath: "personal/project1/backend"}
	tree.Groups["personal/project1"].Sessions = []*Instance{sA}
	tree.Groups["personal/project1/backend"].Sessions = []*Instance{sB}

	if err := tree.MoveGroupTo("personal/project1", "work"); err != nil {
		t.Fatalf("MoveGroupTo returned error: %v", err)
	}

	if tree.Groups["personal/project1"] != nil || tree.Groups["personal/project1/backend"] != nil {
		t.Error("old subgroup paths should be gone")
	}
	if tree.Groups["work/project1"] == nil || tree.Groups["work/project1/backend"] == nil {
		t.Fatal("new subgroup paths should exist")
	}
	if sA.GroupPath != "work/project1" {
		t.Errorf("session A GroupPath = %q, want %q", sA.GroupPath, "work/project1")
	}
	if sB.GroupPath != "work/project1/backend" {
		t.Errorf("session B GroupPath = %q, want %q", sB.GroupPath, "work/project1/backend")
	}
}

// TestMoveGroupTo_DestMissing returns an error when destination parent doesn't exist
// (and is not root/empty).
func TestMoveGroupTo_DestMissing(t *testing.T) {
	tree := NewGroupTree([]*Instance{})
	tree.CreateGroup("personal")

	if err := tree.MoveGroupTo("personal", "nonexistent"); err == nil {
		t.Error("expected error when destination parent doesn't exist")
	}
}

// TestMoveGroupTo_Circular refuses to move a group under itself or a descendant.
func TestMoveGroupTo_Circular(t *testing.T) {
	tree := NewGroupTree([]*Instance{})
	tree.CreateGroup("a")
	tree.CreateSubgroup("a", "b")
	tree.CreateSubgroup("a/b", "c")

	if err := tree.MoveGroupTo("a", "a/b"); err == nil {
		t.Error("expected error moving 'a' under its own descendant 'a/b'")
	}
	if err := tree.MoveGroupTo("a", "a"); err == nil {
		t.Error("expected error moving 'a' under itself")
	}
	if tree.Groups["a"] == nil || tree.Groups["a/b"] == nil || tree.Groups["a/b/c"] == nil {
		t.Error("original structure should be intact after rejected circular move")
	}
}

// TestMoveGroupTo_NoOpSameParent is a no-op when source already under destParent.
func TestMoveGroupTo_NoOpSameParent(t *testing.T) {
	tree := NewGroupTree([]*Instance{})
	tree.CreateGroup("work")
	tree.CreateSubgroup("work", "alpha")

	if err := tree.MoveGroupTo("work/alpha", "work"); err != nil {
		t.Fatalf("MoveGroupTo returned error on same-parent no-op: %v", err)
	}
	if tree.Groups["work/alpha"] == nil {
		t.Error("group should still be at work/alpha after no-op")
	}
}

// TestMoveGroupTo_Collision returns an error when destination already has a group
// with the same base name.
func TestMoveGroupTo_Collision(t *testing.T) {
	tree := NewGroupTree([]*Instance{})
	tree.CreateGroup("personal")
	tree.CreateSubgroup("personal", "alpha")
	tree.CreateGroup("work")
	tree.CreateSubgroup("work", "alpha")

	if err := tree.MoveGroupTo("personal/alpha", "work"); err == nil {
		t.Error("expected collision error moving alpha where work/alpha already exists")
	}
}

// TestMoveGroupTo_SourceMissing returns an error for unknown source.
func TestMoveGroupTo_SourceMissing(t *testing.T) {
	tree := NewGroupTree([]*Instance{})
	if err := tree.MoveGroupTo("nonexistent", ""); err == nil {
		t.Error("expected error when source doesn't exist")
	}
}

// TestMoveGroupTo_DefaultGroupForbidden forbids moving the default group.
func TestMoveGroupTo_DefaultGroupForbidden(t *testing.T) {
	tree := NewGroupTree([]*Instance{})
	tree.CreateGroup("work")
	if err := tree.MoveGroupTo(DefaultGroupPath, "work"); err == nil {
		t.Error("expected error moving the default group")
	}
}
