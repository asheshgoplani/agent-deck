package session

import (
	"strings"
	"testing"
)

// Issue #1878: Flatten() decided group visibility by looking at the immediate
// parent's Expanded flag only. In a hierarchy deeper than two levels, a group
// whose parent was expanded but whose grandparent was collapsed leaked into the
// flattened list and rendered as if it were a root group.
//
// The rule these tests pin down: a group is visible only when EVERY ancestor on
// its path is expanded. Sessions follow their group.

// flatKeys renders a flattened item list as "group:<path>" / "session:<id>"
// strings so expectations read like the rendered deck.
func flatKeys(items []Item) []string {
	keys := make([]string, 0, len(items))
	for _, it := range items {
		switch it.Type {
		case ItemTypeGroup:
			keys = append(keys, "group:"+it.Path)
		case ItemTypeSession:
			id := "<nil>"
			if it.Session != nil {
				id = it.Session.ID
			}
			keys = append(keys, "session:"+id)
		default:
			keys = append(keys, "other")
		}
	}
	return keys
}

func assertFlatKeys(t *testing.T, tree *GroupTree, want []string) {
	t.Helper()
	got := flatKeys(tree.Flatten())
	if len(got) != len(want) || strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("Flatten() =\n  %v\nwant\n  %v", got, want)
	}
}

// buildTree creates every group path in order (intermediate levels included)
// and attaches the given sessions. Every group starts expanded, matching the
// defaults of CreateGroup/CreateSubgroup.
func buildTree(t *testing.T, groupPaths []string, sessions map[string][]string) *GroupTree {
	t.Helper()
	tree := NewGroupTree([]*Instance{})
	for _, p := range groupPaths {
		if g := tree.CreateGroupPath(p); g == nil || g.Path != p {
			t.Fatalf("CreateGroupPath(%q) produced %v, want a group at that exact path", p, g)
		}
	}
	// Deterministic session insertion order: follow groupPaths, not the map.
	for _, p := range groupPaths {
		for _, id := range sessions[p] {
			tree.AddSession(&Instance{ID: id, GroupPath: p})
		}
	}
	return tree
}

// TestFlattenIssue1878CollapsedAncestorHidesSubSubgroup is the issue's exact
// reproduction: three levels, collapse only the top.
func TestFlattenIssue1878CollapsedAncestorHidesSubSubgroup(t *testing.T) {
	tree := NewGroupTree([]*Instance{})
	tree.CreateGroup("Top")
	tree.CreateSubgroup("Top", "Mid")
	tree.CreateSubgroup("Top/Mid", "Leaf")

	tree.ExpandGroup("Top")
	tree.ExpandGroup("Top/Mid")
	tree.ExpandGroup("Top/Mid/Leaf")

	tree.CollapseGroup("Top")

	items := tree.Flatten()
	if len(items) != 1 {
		t.Fatalf("collapsing Top should leave only the Top header; got %d items: %v",
			len(items), flatKeys(items))
	}
	if items[0].Type != ItemTypeGroup || items[0].Path != "Top" {
		t.Fatalf("expected the Top group header, got %+v", items[0])
	}
}

// TestFlattenAncestorVisibility is the table test: (hierarchy, collapsed set) →
// exact visible rows.
func TestFlattenAncestorVisibility(t *testing.T) {
	// A five-level chain plus a sibling branch and an unrelated root, with a
	// session at every level, exercises depth, sibling isolation and
	// session-follows-group in one fixture.
	deepGroups := []string{"a", "a/b", "a/b/c", "a/b/c/d", "a/b/c/d/e"}
	deepSessions := map[string][]string{
		"a":         {"s-a"},
		"a/b":       {"s-b"},
		"a/b/c":     {"s-c"},
		"a/b/c/d":   {"s-d"},
		"a/b/c/d/e": {"s-e"},
	}
	allDeepVisible := []string{
		"group:a", "session:s-a",
		"group:a/b", "session:s-b",
		"group:a/b/c", "session:s-c",
		"group:a/b/c/d", "session:s-d",
		"group:a/b/c/d/e", "session:s-e",
	}

	tests := []struct {
		name      string
		groups    []string
		sessions  map[string][]string
		collapsed []string
		want      []string
	}{
		{
			name:     "five levels all expanded",
			groups:   deepGroups,
			sessions: deepSessions,
			want:     allDeepVisible,
		},
		{
			name:      "collapse level 0 hides the whole subtree",
			groups:    deepGroups,
			sessions:  deepSessions,
			collapsed: []string{"a"},
			want:      []string{"group:a"},
		},
		{
			name:      "collapse level 1 hides everything below it",
			groups:    deepGroups,
			sessions:  deepSessions,
			collapsed: []string{"a/b"},
			want:      []string{"group:a", "session:s-a", "group:a/b"},
		},
		{
			name:      "collapse level 2 hides everything below it",
			groups:    deepGroups,
			sessions:  deepSessions,
			collapsed: []string{"a/b/c"},
			want: []string{
				"group:a", "session:s-a",
				"group:a/b", "session:s-b",
				"group:a/b/c",
			},
		},
		{
			name:      "collapse level 3 hides everything below it",
			groups:    deepGroups,
			sessions:  deepSessions,
			collapsed: []string{"a/b/c/d"},
			want: []string{
				"group:a", "session:s-a",
				"group:a/b", "session:s-b",
				"group:a/b/c", "session:s-c",
				"group:a/b/c/d",
			},
		},
		{
			name:      "collapse the deepest leaf hides only its sessions",
			groups:    deepGroups,
			sessions:  deepSessions,
			collapsed: []string{"a/b/c/d/e"},
			want: []string{
				"group:a", "session:s-a",
				"group:a/b", "session:s-b",
				"group:a/b/c", "session:s-c",
				"group:a/b/c/d", "session:s-d",
				"group:a/b/c/d/e",
			},
		},
		{
			name:      "collapsing an ancestor wins over expanded descendants",
			groups:    deepGroups,
			sessions:  deepSessions,
			collapsed: []string{"a/b"},
			// a/b/c, a/b/c/d and a/b/c/d/e stay Expanded==true; they must
			// still be hidden because a/b is collapsed. This is the exact
			// leak from #1878, one level deeper.
			want: []string{"group:a", "session:s-a", "group:a/b"},
		},
		{
			name:      "collapse a middle group with expanded groups above and below",
			groups:    []string{"top", "top/mid", "top/mid/leaf", "top/other", "top/other/leaf"},
			sessions:  map[string][]string{"top": {"s1"}, "top/mid": {"s2"}, "top/mid/leaf": {"s3"}, "top/other": {"s4"}, "top/other/leaf": {"s5"}},
			collapsed: []string{"top/mid"},
			want: []string{
				"group:top", "session:s1",
				"group:top/mid",
				"group:top/other", "session:s4",
				"group:top/other/leaf", "session:s5",
			},
		},
		{
			name:      "collapsing one root leaves an unrelated root untouched",
			groups:    []string{"alpha", "alpha/one", "alpha/one/two", "beta", "beta/one"},
			sessions:  map[string][]string{"alpha/one/two": {"s-deep"}, "beta/one": {"s-beta"}},
			collapsed: []string{"alpha"},
			want: []string{
				"group:alpha",
				"group:beta",
				"group:beta/one", "session:s-beta",
			},
		},
		{
			name:      "two collapsed ancestors on the same chain",
			groups:    deepGroups,
			sessions:  deepSessions,
			collapsed: []string{"a/b", "a/b/c/d"},
			want:      []string{"group:a", "session:s-a", "group:a/b"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tree := buildTree(t, tc.groups, tc.sessions)
			for _, p := range tc.collapsed {
				if _, ok := tree.Groups[p]; !ok {
					t.Fatalf("test fixture collapses unknown group %q", p)
				}
				tree.CollapseGroup(p)
			}
			assertFlatKeys(t, tree, tc.want)
		})
	}
}

// TestFlattenCollapseExpandRoundTrip verifies that collapsing then re-expanding
// a group restores the original flattening exactly — no state is lost or
// cascaded into descendants.
func TestFlattenCollapseExpandRoundTrip(t *testing.T) {
	groups := []string{"root", "root/mid", "root/mid/leaf", "root/mid/leaf/deep"}
	sessions := map[string][]string{
		"root":               {"r1", "r2"},
		"root/mid":           {"m1"},
		"root/mid/leaf":      {"l1"},
		"root/mid/leaf/deep": {"d1"},
	}
	tree := buildTree(t, groups, sessions)

	before := flatKeys(tree.Flatten())

	for _, p := range groups {
		tree.CollapseGroup(p)
		tree.ExpandGroup(p)
		assertFlatKeys(t, tree, before)
	}

	// Collapse every level, then expand every level, and land back where we
	// started.
	for _, p := range groups {
		tree.CollapseGroup(p)
	}
	if got := flatKeys(tree.Flatten()); len(got) != 1 || got[0] != "group:root" {
		t.Fatalf("all-collapsed flatten = %v, want [group:root]", got)
	}
	for _, p := range groups {
		tree.ExpandGroup(p)
	}
	assertFlatKeys(t, tree, before)

	// ToggleGroup twice is the same round trip through the TUI's entry point.
	tree.ToggleGroup("root/mid")
	tree.ToggleGroup("root/mid")
	assertFlatKeys(t, tree, before)
}

// TestFlattenEveryRowHasAVisibleParentRow guards the user-visible symptom:
// no row may be emitted whose group path has no visible parent row. This is what
// made #1878 render an orphan subgroup at the top level.
func TestFlattenEveryRowHasAVisibleParentRow(t *testing.T) {
	tree := buildTree(t,
		[]string{"p", "p/q", "p/q/r", "p/q/r/s", "z", "z/y"},
		map[string][]string{"p/q/r": {"s1"}, "p/q/r/s": {"s2"}, "z/y": {"s3"}},
	)
	tree.CollapseGroup("p/q")

	visible := map[string]bool{}
	for _, it := range tree.Flatten() {
		if it.Type != ItemTypeGroup {
			continue
		}
		visible[it.Path] = true
		if parent := getParentPath(it.Path); parent != "" && !visible[parent] {
			t.Errorf("group %q rendered without a visible parent row %q (flatten: %v)",
				it.Path, parent, flatKeys(tree.Flatten()))
		}
	}
}

// TestFlattenMissingIntermediateGroupStaysVisible pins the pre-existing
// tolerance for a dangling path: if an intermediate group is absent from the
// Groups map, its descendants keep rendering rather than vanishing. Only a
// group that actually exists and is collapsed hides its subtree.
func TestFlattenMissingIntermediateGroupStaysVisible(t *testing.T) {
	tree := buildTree(t, []string{"m", "m/n", "m/n/o"}, nil)
	delete(tree.Groups, "m/n")
	tree.rebuildGroupList()

	assertFlatKeys(t, tree, []string{"group:m", "group:m/n/o"})

	// ...but a collapsed grandparent still hides it, even across the gap.
	tree.CollapseGroup("m")
	assertFlatKeys(t, tree, []string{"group:m"})
}

// BenchmarkFlattenDeepHierarchy keeps the render path honest: Flatten() runs on
// every frame, and the ancestor-chain visibility check must stay linear in the
// tree rather than becoming O(groups × depth) string rescanning.
func BenchmarkFlattenDeepHierarchy(b *testing.B) {
	tree := benchTree()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tree.Flatten()
	}
}

// BenchmarkFlattenDeepHierarchyCollapsed is the same tree with a collapsed
// group, which is what actually engages the ancestor-chain walk.
func BenchmarkFlattenDeepHierarchyCollapsed(b *testing.B) {
	tree := benchTree()
	tree.CollapseGroup("roota0/level1/level2")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tree.Flatten()
	}
}

func benchTree() *GroupTree {
	tree := NewGroupTree([]*Instance{})
	// 40 root branches × 6 levels deep = 240 groups, each with a session.
	for r := 0; r < 40; r++ {
		path := ""
		for d := 0; d < 6; d++ {
			if path == "" {
				path = "root" + string(rune('a'+r%26)) + string(rune('0'+r/26))
			} else {
				path = path + "/level" + string(rune('0'+d))
			}
			tree.CreateGroupPath(path)
			tree.AddSession(&Instance{ID: path + "/s", GroupPath: path})
		}
	}
	return tree
}

// TestFlattenMalformedPathSegments covers group paths that the normal
// create/rename entry points cannot produce (sanitizeGroupName strips "/" from
// names) but that could arrive from hand-edited or legacy persisted state:
// empty segments and a trailing separator. The ancestor walk must still find
// the real collapsed ancestor rather than being defeated by the odd segment.
func TestFlattenMalformedPathSegments(t *testing.T) {
	for _, child := range []string{"top//weird", "top/"} {
		t.Run(child, func(t *testing.T) {
			tree := NewGroupTree([]*Instance{})
			tree.CreateGroup("top")
			tree.Groups[child] = &Group{Name: "weird", Path: child, Expanded: true}
			tree.Expanded[child] = true
			tree.rebuildGroupList()

			tree.CollapseGroup("top")
			got := flatKeys(tree.Flatten())
			for _, k := range got {
				if k == "group:"+child {
					t.Errorf("group %q leaked while its ancestor %q is collapsed: %v", child, "top", got)
				}
			}
		})
	}
}
