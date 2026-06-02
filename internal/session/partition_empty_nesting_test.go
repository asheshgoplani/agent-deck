package session

import (
	"strings"
	"testing"
)

// assertBottomAncestorsNested verifies the partition invariant: every group row
// in the bottom section (after the divider) has its full ancestor-path chain
// present as group rows earlier in that same section. A nested empty group must
// never render orphaned with an indent but no parent header above it.
func assertBottomAncestorsNested(t *testing.T, out []Item) {
	t.Helper()
	seenBottom := map[string]bool{}
	inBottom := false
	for _, it := range out {
		if it.Type == ItemTypeDivider {
			inBottom = true
			continue
		}
		if !inBottom || it.Type != ItemTypeGroup {
			continue
		}
		// Every strict ancestor must already have appeared in the bottom.
		parts := strings.Split(it.Path, "/")
		for i := 1; i < len(parts); i++ {
			anc := strings.Join(parts[:i], "/")
			if !seenBottom[anc] {
				t.Errorf("bottom group %q is orphaned: ancestor %q not shown above it", it.Path, anc)
			}
		}
		seenBottom[it.Path] = true
	}
}

// TestPartitionEmptyChildOfActivityPopulatedParentNestsUnderParent reproduces
// the image bug: a parent (fjordbyte) is populated only via sessions that are
// NOT visible rows in the flattened list (e.g. stopped/filtered/collapsed), so
// it is "populated" via the activity map but has no hasTopRow marker. Its empty
// child (canvas-azure) must still sink to the bottom NESTED under a re-shown
// fjordbyte header, not orphaned.
func TestPartitionEmptyChildOfActivityPopulatedParentNestsUnderParent(t *testing.T) {
	items := []Item{
		{Type: ItemTypeGroup, Path: "doozyx", Level: 0},
		{Type: ItemTypeGroup, Path: "doozyx/agent-deck", Level: 1},
		sessItem("a1", StatusRunning, "doozyx/agent-deck"),
		// fjordbyte's only sessions are stopped -> filtered out of the visible
		// list, so no session row appears here. Its empty child follows.
		{Type: ItemTypeGroup, Path: "fjordbyte", Level: 0},
		{Type: ItemTypeGroup, Path: "fjordbyte/canvas-azure", Level: 1},
	}
	activity := map[string]GroupActivity{
		"doozyx":            {HasAny: true, HasActive: true},
		"doozyx/agent-deck": {HasAny: true, HasActive: true},
		// Populated via stopped sessions: HasAny true, HasActive false.
		"fjordbyte": {HasAny: true},
	}
	out := PartitionByViewMode(items, GroupViewPopulatedTop, activity)
	assertBottomAncestorsNested(t, out)
}

// TestPartitionEmptyChildOfCollapsedPopulatedParentNestsUnderParent covers the
// collapsed-populated-parent variant: doozyx is populated only through a
// collapsed subgroup (doozyx/core: header present, sessions hidden). Its empty
// siblings baba/infra must nest under a re-shown doozyx header in the bottom.
func TestPartitionEmptyChildOfCollapsedPopulatedParentNestsUnderParent(t *testing.T) {
	items := []Item{
		{Type: ItemTypeGroup, Path: "doozyx", Level: 0},
		{Type: ItemTypeGroup, Path: "doozyx/core", Level: 1}, // collapsed populated: header only
		{Type: ItemTypeGroup, Path: "doozyx/baba", Level: 1},
		{Type: ItemTypeGroup, Path: "doozyx/infra", Level: 1},
		{Type: ItemTypeGroup, Path: "adaptam", Level: 0},
		sessItem("1", StatusIdle, "adaptam"),
	}
	activity := map[string]GroupActivity{
		"doozyx":      {HasAny: true, HasActive: true},
		"doozyx/core": {HasAny: true, HasActive: true},
		"adaptam":     {HasAny: true},
	}
	out := PartitionByViewMode(items, GroupViewPopulatedTop, activity)
	assertBottomAncestorsNested(t, out)
}
