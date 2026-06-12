package ui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

var groupNestingANSIStripRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSIForGroupNesting(s string) string {
	return groupNestingANSIStripRe.ReplaceAllString(s, "")
}

// triangleCol returns the rune column of the first expand triangle (▾/▸) on a
// rendered group line, or -1 if none.
func triangleCol(line string) int {
	clean := stripANSIForGroupNesting(line)
	for _, marker := range []string{"▾", "▸"} {
		if i := strings.Index(clean, marker); i >= 0 {
			return len([]rune(clean[:i]))
		}
	}
	return -1
}

// TestGroupNestingIndent verifies that a group's expand-triangle column reflects
// its nesting level regardless of whether the root carries a hotkey number.
// Before the hotkey-gutter fix, a numbered root group ("N·▾") put its triangle
// at the same column as its (indented) child, rendering the subtree flat. The
// fix reserves a fixed-width gutter so triangle column == gutter + level*indent.
func TestGroupNestingIndent(t *testing.T) {
	inst := session.NewInstanceWithTool("Add copy session", "/tmp/doozyx/agent-deck", "claude")
	inst.GroupPath = "doozyx/agent-deck"
	instances := []*session.Instance{inst}

	groups := []*session.GroupData{
		{Name: "doozyx", Path: "doozyx", Expanded: true, Order: 0},
		{Name: "agent-deck", Path: "doozyx/agent-deck", Expanded: true, Order: 0},
		{Name: "baba", Path: "doozyx/baba", Expanded: true, Order: 1},
		{Name: "infra", Path: "doozyx/infra", Expanded: true, Order: 2},
	}

	home := NewHome()
	home.width = 120
	home.height = 60
	home.initialLoading = false
	home.instancesMu.Lock()
	home.instances = instances
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTreeWithGroups(instances, groups)
	home.groupViewMode = session.GroupViewPopulatedTop
	home.cursor = -1 // nothing selected, so root hotkeys render
	home.rebuildFlatItems()

	rendered := home.renderSessionList(120, 60)

	// Focus on the bottom ("empty groups") section, where the numbered root
	// doozyx and its empty children baba/infra appear. This is where a numbered
	// root previously rendered flat. Collect triangle columns there.
	cols := map[string]int{}
	belowDivider := false
	for _, line := range strings.Split(rendered, "\n") {
		clean := stripANSIForGroupNesting(line)
		if strings.Contains(clean, "empty groups") {
			belowDivider = true
			continue
		}
		if !belowDivider {
			continue
		}
		for _, name := range []string{"doozyx", "baba", "infra"} {
			if !strings.Contains(clean, name+" (") {
				continue
			}
			if _, seen := cols[name]; seen {
				continue
			}
			if c := triangleCol(line); c >= 0 {
				cols[name] = c
			}
		}
	}

	// A numbered level-0 root and its level-1 children must NOT share a triangle
	// column — children must be visibly nested (indented further right).
	root, ok := cols["doozyx"]
	if !ok {
		t.Fatalf("missing row doozyx\n--- render ---\n%s", stripANSIForGroupNesting(rendered))
	}
	for _, child := range []string{"baba", "infra"} {
		if _, ok := cols[child]; !ok {
			t.Fatalf("missing row %s\n--- render ---\n%s", child, stripANSIForGroupNesting(rendered))
		}
		if cols[child] <= root {
			t.Errorf("child %q triangle col %d should be > root doozyx col %d (nested, not flat)\n--- render ---\n%s",
				child, cols[child], root, stripANSIForGroupNesting(rendered))
		}
	}
}

func TestDuplicateRootHeadersReuseRootGroupNumber(t *testing.T) {
	inst := session.NewInstanceWithTool("Add copy session", "/tmp/doozyx/agent-deck", "claude")
	inst.GroupPath = "doozyx/agent-deck"
	instances := []*session.Instance{inst}

	groups := []*session.GroupData{
		{Name: "doozyx", Path: "doozyx", Expanded: true, Order: 0},
		{Name: "agent-deck", Path: "doozyx/agent-deck", Expanded: true, Order: 0},
		{Name: "baba", Path: "doozyx/baba", Expanded: true, Order: 1},
	}

	home := NewHome()
	home.width = 120
	home.height = 60
	home.initialLoading = false
	home.instancesMu.Lock()
	home.instances = instances
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTreeWithGroups(instances, groups)
	home.groupViewMode = session.GroupViewPopulatedTop
	home.rebuildFlatItems()

	var nums []int
	for _, it := range home.flatItems {
		if it.Type == session.ItemTypeGroup && it.Level == 0 && it.Path == "doozyx" {
			nums = append(nums, it.RootGroupNum)
		}
	}
	if len(nums) < 2 {
		t.Fatalf("expected duplicate doozyx headers in partitioned view, got %v", nums)
	}
	for _, n := range nums[1:] {
		if n != nums[0] {
			t.Fatalf("duplicate root headers must reuse root number, got %v", nums)
		}
	}
}
