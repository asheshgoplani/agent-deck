package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Repro mirroring live structure:
//   - "adaptam" : parent group, 0 DIRECT sessions
//   - "adaptam/ui" : populated child (live sessions)
//   - "truly-empty" : standalone group, 0 sessions anywhere in subtree
func TestReproParentWithPopulatedChild(t *testing.T) {
	home := NewHome()
	home.width = 120
	home.height = 40
	home.initialLoading = false

	instances := []*session.Instance{
		session.NewInstanceWithTool("u1", "/tmp/u1", "claude"),
		session.NewInstanceWithTool("u2", "/tmp/u2", "claude"),
		session.NewInstanceWithTool("top1", "/tmp/top1", "claude"),
	}
	instances[0].GroupPath = "adaptam/ui"
	instances[1].GroupPath = "adaptam/ui"
	instances[2].GroupPath = "my-sessions"

	home.instancesMu.Lock()
	home.instances = instances
	home.instancesMu.Unlock()

	// Build via stored-groups path so the empty parent + standalone empty persist,
	// like a real DB load (NewGroupTreeWithGroups).
	stored := []*session.GroupData{
		{Name: "adaptam", Path: "adaptam", Expanded: true},
		{Name: "ui", Path: "adaptam/ui", Expanded: true},
		{Name: "my-sessions", Path: "my-sessions", Expanded: true},
		{Name: "truly-empty", Path: "truly-empty", Expanded: true},
	}
	home.groupTree = session.NewGroupTreeWithGroups(instances, stored)

	home.groupViewMode = session.GroupViewPopulatedTop
	home.rebuildFlatItems()

	div := dividerIndex(home)
	t.Logf("divider at %d, total %d", div, len(home.flatItems))
	for i, it := range home.flatItems {
		typ := "?"
		switch it.Type {
		case session.ItemTypeGroup:
			typ = "GROUP"
		case session.ItemTypeSession:
			typ = "sess"
		case session.ItemTypeDivider:
			typ = "----DIVIDER----"
		}
		title := it.Path
		if it.Session != nil {
			title = it.Session.Title
		}
		t.Logf("  [%d] %s %q", i, typ, title)
	}
}
