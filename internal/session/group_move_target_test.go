package session

import "testing"

// ResolveMoveTargetGroup is shared by `agent-deck group move` and the web
// move endpoint (#2368), so both surfaces resolve a target the same way.
func TestResolveMoveTargetGroup(t *testing.T) {
	tree := NewGroupTree(nil)
	tree.CreateGroup("Work")

	cases := []struct {
		target, want string
	}{
		{"", DefaultGroupPath},
		{"root", DefaultGroupPath},
		{DefaultGroupPath, DefaultGroupPath},
		{"Work", "Work"},
		{"work", "Work"},
		{"WORK", "Work"},
		{"new team", "new-team"},
	}
	for _, tc := range cases {
		if got := tree.ResolveMoveTargetGroup(tc.target); got != tc.want {
			t.Errorf("ResolveMoveTargetGroup(%q) = %q, want %q", tc.target, got, tc.want)
		}
	}
	if _, ok := tree.Groups["new-team"]; !ok {
		t.Fatal("a missing target group must be created")
	}
}
