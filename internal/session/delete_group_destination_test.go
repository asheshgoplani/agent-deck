package session

import "testing"

// Deleting a group moves its sessions (and those of its subgroups) to the
// parent group, and to the default group only when a root group is deleted.
// The TUI and the web used to send them to the default group in both cases
// while `group delete --force` sent them to the parent.
func TestDeleteGroup_SessionsMoveToParentElseDefault(t *testing.T) {
	inSub := NewInstanceWithGroup("s-sub", "/tmp/p", "work/sub")
	inDeep := NewInstanceWithGroup("s-deep", "/tmp/p", "work/sub/deep")
	inWork := NewInstanceWithGroup("s-work", "/tmp/p", "work")
	tree := NewGroupTree([]*Instance{inSub, inDeep, inWork})

	moved := tree.DeleteGroup("work/sub")
	if len(moved) != 2 {
		t.Fatalf("moved %d sessions, want 2", len(moved))
	}
	for _, inst := range []*Instance{inSub, inDeep} {
		if inst.GroupPath != "work" {
			t.Errorf("%s is in %q after deleting work/sub, want the parent group work", inst.Title, inst.GroupPath)
		}
	}
	if got := len(tree.Groups["work"].Sessions); got != 3 {
		t.Errorf("work holds %d sessions, want 3", got)
	}
	if _, ok := tree.Groups[DefaultGroupPath]; ok {
		t.Error("deleting a subgroup created the default group")
	}

	tree.DeleteGroup("work")
	for _, inst := range []*Instance{inSub, inDeep, inWork} {
		if inst.GroupPath != DefaultGroupPath {
			t.Errorf("%s is in %q after deleting root group work, want %q", inst.Title, inst.GroupPath, DefaultGroupPath)
		}
	}
}
