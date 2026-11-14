package session

import (
	"testing"
)

// Issue #2995: a sub-session whose parent is in another group, or whose parent
// no longer exists, is drawn by Flatten as a top-level row of its own group.
// MoveSessionUp/Down must treat it the same way: it reorders among the group's
// top-level rows, and top-level rows reorder past it. Before the fix the move
// functions looked only for siblings sharing its ParentSessionID, found none,
// and Shift+Up/Down (and Ctrl+Up, K, J) silently did nothing.

// flattenedSessionIDs returns the session rows Flatten draws for group path,
// in display order.
func flattenedSessionIDs(tree *GroupTree, path string) []string {
	var ids []string
	for _, it := range tree.Flatten() {
		if it.Type == ItemTypeSession && it.Session != nil && it.Path == path {
			ids = append(ids, it.Session.ID)
		}
	}
	return ids
}

func TestMoveSession_OrphanedSubSessionReordersAsTopLevel_Issue2995(t *testing.T) {
	t.Cleanup(func() { SetGroupSortMode("creation") })
	SetGroupSortMode("creation")

	// Each case builds group "beta". "parent" lives in group "alpha" (case
	// "parent in another group") or nowhere at all (case "parent deleted").
	type move struct {
		id   string
		down bool
	}
	cases := []struct {
		name       string
		withParent bool // put "parent" in group alpha
		beta       []*Instance
		before     []string // drawn order of beta before the move
		move       move
		want       []string // drawn order of beta after the move
		wantParent map[string]string
	}{
		{
			name:       "parent in another group: child moves up past a top-level peer",
			withParent: true,
			beta: []*Instance{
				{ID: "peer", Order: 0},
				{ID: "child", Order: 1, ParentSessionID: "parent"},
			},
			before:     []string{"peer", "child"},
			move:       move{id: "child"},
			want:       []string{"child", "peer"},
			wantParent: map[string]string{"child": "parent"},
		},
		{
			name: "parent deleted: child moves up past a top-level peer",
			beta: []*Instance{
				{ID: "peer", Order: 0},
				{ID: "child", Order: 1, ParentSessionID: "parent"},
			},
			before:     []string{"peer", "child"},
			move:       move{id: "child"},
			want:       []string{"child", "peer"},
			wantParent: map[string]string{"child": "parent"},
		},
		{
			name:       "parent in another group: child moves down past a top-level peer",
			withParent: true,
			beta: []*Instance{
				{ID: "child", Order: 0, ParentSessionID: "parent"},
				{ID: "peer", Order: 1},
			},
			before:     []string{"child", "peer"},
			move:       move{id: "child", down: true},
			want:       []string{"peer", "child"},
			wantParent: map[string]string{"child": "parent"},
		},
		{
			name: "parent deleted: child moves down past a top-level peer",
			beta: []*Instance{
				{ID: "child", Order: 0, ParentSessionID: "parent"},
				{ID: "peer", Order: 1},
			},
			before:     []string{"child", "peer"},
			move:       move{id: "child", down: true},
			want:       []string{"peer", "child"},
			wantParent: map[string]string{"child": "parent"},
		},
		{
			name:       "top-level peer moves down past an orphaned child",
			withParent: true,
			beta: []*Instance{
				{ID: "peer", Order: 0},
				{ID: "child", Order: 1, ParentSessionID: "parent"},
			},
			before:     []string{"peer", "child"},
			move:       move{id: "peer", down: true},
			want:       []string{"child", "peer"},
			wantParent: map[string]string{"child": "parent"},
		},
		{
			name: "two orphans with different missing parents swap",
			beta: []*Instance{
				{ID: "o1", Order: 0, ParentSessionID: "gone-1"},
				{ID: "o2", Order: 1, ParentSessionID: "gone-2"},
			},
			before:     []string{"o1", "o2"},
			move:       move{id: "o2"},
			want:       []string{"o2", "o1"},
			wantParent: map[string]string{"o1": "gone-1", "o2": "gone-2"},
		},
		{
			name:       "orphan at the top of the group: up is a no-op and keeps its parent",
			withParent: true,
			beta: []*Instance{
				{ID: "child", Order: 0, ParentSessionID: "parent"},
				{ID: "peer", Order: 1},
			},
			before:     []string{"child", "peer"},
			move:       move{id: "child"},
			want:       []string{"child", "peer"},
			wantParent: map[string]string{"child": "parent"},
		},
		{
			name: "orphan at the bottom of the group: down is a no-op and keeps its parent",
			beta: []*Instance{
				{ID: "peer", Order: 0},
				{ID: "child", Order: 1, ParentSessionID: "parent"},
			},
			before:     []string{"peer", "child"},
			move:       move{id: "child", down: true},
			want:       []string{"peer", "child"},
			wantParent: map[string]string{"child": "parent"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var instances []*Instance
			if tc.withParent {
				instances = append(instances, &Instance{ID: "parent", Title: "parent", GroupPath: "alpha"})
			}
			byID := map[string]*Instance{}
			for _, in := range tc.beta {
				in.Title, in.GroupPath = in.ID, "beta"
				byID[in.ID] = in
				instances = append(instances, in)
			}
			tree := NewGroupTree(instances)

			if got := flattenedSessionIDs(tree, "beta"); !equalStrings(got, tc.before) {
				t.Fatalf("precondition: beta drawn as %v, want %v", got, tc.before)
			}

			if tc.move.down {
				tree.MoveSessionDown(byID[tc.move.id])
			} else {
				tree.MoveSessionUp(byID[tc.move.id])
			}

			if got := flattenedSessionIDs(tree, "beta"); !equalStrings(got, tc.want) {
				t.Errorf("beta drawn as %v after moving %q, want %v", got, tc.move.id, tc.want)
			}
			for id, parent := range tc.wantParent {
				if got := byID[id].ParentSessionID; got != parent {
					t.Errorf("%s.ParentSessionID = %q after the move, want %q (a reorder must not drop the link)", id, got, parent)
				}
			}
			// The new order must survive a reload, which rebuilds the tree
			// from Order alone.
			reloaded := NewGroupTree(instances)
			if got := flattenedSessionIDs(reloaded, "beta"); !equalStrings(got, tc.want) {
				t.Errorf("after reload beta drawn as %v, want %v", got, tc.want)
			}
		})
	}
}

// Nested sub-sessions are untouched by #2995: a child whose parent is a
// top-level row of the same group still reorders only among its siblings and
// is still drawn right under its parent.
func TestMoveSession_NestedChildStillMovesAmongSiblings_Issue2995(t *testing.T) {
	t.Cleanup(func() { SetGroupSortMode("creation") })
	SetGroupSortMode("creation")

	instances := []*Instance{
		{ID: "p", Title: "p", GroupPath: "g", Order: 0},
		{ID: "c1", Title: "c1", GroupPath: "g", Order: 1, ParentSessionID: "p"},
		{ID: "o", Title: "o", GroupPath: "g", Order: 2, ParentSessionID: "gone"},
		{ID: "c2", Title: "c2", GroupPath: "g", Order: 3, ParentSessionID: "p"},
	}
	tree := NewGroupTree(instances)
	if got, want := flattenedSessionIDs(tree, "g"), []string{"p", "c1", "c2", "o"}; !equalStrings(got, want) {
		t.Fatalf("precondition: drawn as %v, want %v", got, want)
	}

	tree.MoveSessionUp(instances[3]) // c2 swaps with c1, skipping the orphan
	if got, want := flattenedSessionIDs(tree, "g"), []string{"p", "c2", "c1", "o"}; !equalStrings(got, want) {
		t.Errorf("after MoveSessionUp(c2) drawn as %v, want %v", got, want)
	}
	if instances[3].ParentSessionID != "p" {
		t.Errorf("c2 lost its parent: %q", instances[3].ParentSessionID)
	}

	tree.MoveSessionUp(instances[2]) // orphan swaps with p, the previous top-level row
	if got, want := flattenedSessionIDs(tree, "g"), []string{"o", "p", "c2", "c1"}; !equalStrings(got, want) {
		t.Errorf("after MoveSessionUp(o) drawn as %v, want %v", got, want)
	}
}
