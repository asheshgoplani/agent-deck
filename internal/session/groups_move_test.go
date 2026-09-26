package session

import "testing"

// rootOrder returns the display order of root-level group paths.
func rootOrder(t *GroupTree) []string {
	var paths []string
	for _, g := range t.GroupList {
		if getParentPath(g.Path) == "" {
			paths = append(paths, g.Path)
		}
	}
	return paths
}

func indexOf(paths []string, want string) int {
	for i, p := range paths {
		if p == want {
			return i
		}
	}
	return -1
}

// TestMoveGroupUpPastSiblingWithSubgroups reproduces the real deck layout where a
// root group sits directly below another root group that has subgroups. The
// subgroups are interleaved between the two in the flat GroupList, so a move that
// only inspects the immediately adjacent slice entry never finds a sibling.
func TestMoveGroupUpPastSiblingWithSubgroups(t *testing.T) {
	tree := NewGroupTree([]*Instance{})

	tree.CreateGroup("todolist")
	tree.CreateSubgroup("todolist", "2026-08-14")
	tree.CreateSubgroup("todolist", "2026-08-15")
	tree.CreateGroup("ai")
	tree.Groups["todolist"].Order = 10
	tree.Groups["ai"].Order = 19
	tree.rebuildGroupList()

	before := rootOrder(tree)
	if indexOf(before, "todolist") >= indexOf(before, "ai") {
		t.Fatalf("precondition: expected todolist above ai, got %v", before)
	}

	tree.MoveGroupUp("ai")

	after := rootOrder(tree)
	if indexOf(after, "ai") >= indexOf(after, "todolist") {
		t.Fatalf("expected ai to move above todolist, got %v", after)
	}
}

// TestMoveGroupDownPastSiblingWithSubgroups is the mirror case: the group being
// moved down has its own subgroups, so its own children occupy the adjacent slot
// in the flat list rather than the next root sibling.
func TestMoveGroupDownPastSiblingWithSubgroups(t *testing.T) {
	tree := NewGroupTree([]*Instance{})

	tree.CreateGroup("todolist")
	tree.CreateSubgroup("todolist", "2026-08-14")
	tree.CreateSubgroup("todolist", "2026-08-15")
	tree.CreateGroup("ai")
	tree.Groups["todolist"].Order = 10
	tree.Groups["ai"].Order = 19
	tree.rebuildGroupList()

	tree.MoveGroupDown("todolist")

	after := rootOrder(tree)
	if indexOf(after, "ai") >= indexOf(after, "todolist") {
		t.Fatalf("expected todolist to move below ai, got %v", after)
	}
}

// TestMoveGroupWithEqualOrders covers groups that share a persisted Order (the
// common case: everything created before ordering existed is Order 0 and is
// tie-broken by name). Swapping equal Order values would be a no-op.
func TestMoveGroupWithEqualOrders(t *testing.T) {
	tree := NewGroupTree([]*Instance{})

	for _, name := range []string{"accounting", "infra", "video"} {
		tree.CreateGroup(name)
		tree.Groups[name].Order = 0
	}
	tree.rebuildGroupList()

	if got := rootOrder(tree); got[0] != "accounting" || got[1] != "infra" || got[2] != "video" {
		t.Fatalf("precondition: expected name-sorted order, got %v", got)
	}

	tree.MoveGroupUp("video")

	after := rootOrder(tree)
	if after[1] != "video" || after[2] != "infra" {
		t.Fatalf("expected video to move above infra, got %v", after)
	}
}

// TestMoveGroupSubgroupAmongSiblings verifies subgroups still reorder among their
// own siblings, and that the whole subtree stays under its parent.
func TestMoveGroupSubgroupAmongSiblings(t *testing.T) {
	tree := NewGroupTree([]*Instance{})

	tree.CreateGroup("todolist")
	tree.CreateSubgroup("todolist", "aaa")
	tree.CreateSubgroup("todolist", "bbb")
	tree.CreateGroup("zzz")
	tree.rebuildGroupList()

	tree.MoveGroupUp("todolist/bbb")

	var paths []string
	for _, g := range tree.GroupList {
		paths = append(paths, g.Path)
	}
	want := []string{"todolist", "todolist/bbb", "todolist/aaa", "zzz"}
	for i, w := range want {
		if i >= len(paths) || paths[i] != w {
			t.Fatalf("expected %v, got %v", want, paths)
		}
	}
}

// TestMoveGroupUpAtTopIsNoOp guards the boundary: the first sibling has nowhere
// to go and must not steal a non-sibling's slot.
func TestMoveGroupUpAtTopIsNoOp(t *testing.T) {
	tree := NewGroupTree([]*Instance{})

	tree.CreateGroup("alpha")
	tree.CreateGroup("beta")
	tree.rebuildGroupList()

	tree.MoveGroupUp("alpha")

	if got := rootOrder(tree); got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("expected order unchanged, got %v", got)
	}
}
