package main

import "testing"

// TestShouldWarnOrphanWorktreeGroup pins the orphan-worktree group guard.
//
// Bug this guards against: a conductor's orchestrate/fleet children were
// launched without a parent attached (parent_id=null) — an auto-parent miss
// (launch issued from a shell lacking $AGENTDECK_INSTANCE_ID, e.g. a subagent).
// With no parent to inherit from, each worktree child fell back to its
// branch-leaf cwd-derived group (a stray `feature-x` group next to the
// conductor's group) with nothing surfacing it at launch time.
//
// The guard warns exactly when a linked-worktree child has no parent and no
// explicit -g. It must NOT warn for #972's non-worktree conductor children,
// which legitimately take their cwd-derived project group.
func TestShouldWarnOrphanWorktreeGroup(t *testing.T) {
	tests := []struct {
		name                  string
		parentAttached        bool
		explicitGroupProvided bool
		pathIsLinkedWorktree  bool
		want                  bool
	}{
		{
			name:                 "orphan worktree child: no parent, no -g, linked worktree -> warn",
			parentAttached:       false,
			pathIsLinkedWorktree: true,
			want:                 true,
		},
		{
			name:                 "worktree child that parented -> no warn (inherit fired)",
			parentAttached:       true,
			pathIsLinkedWorktree: true,
			want:                 false,
		},
		{
			name:                  "explicit -g -> no warn (deliberate placement)",
			explicitGroupProvided: true,
			pathIsLinkedWorktree:  true,
			want:                  false,
		},
		{
			name:                 "non-worktree conductor child (#972) -> no warn",
			parentAttached:       false,
			pathIsLinkedWorktree: false,
			want:                 false,
		},
		{
			name:                 "non-worktree child with parent -> no warn",
			parentAttached:       true,
			pathIsLinkedWorktree: false,
			want:                 false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldWarnOrphanWorktreeGroup(tt.parentAttached, tt.explicitGroupProvided, tt.pathIsLinkedWorktree)
			if got != tt.want {
				t.Fatalf("shouldWarnOrphanWorktreeGroup(parent=%v, explicit=%v, worktree=%v) = %v, want %v",
					tt.parentAttached, tt.explicitGroupProvided, tt.pathIsLinkedWorktree, got, tt.want)
			}
		})
	}
}
