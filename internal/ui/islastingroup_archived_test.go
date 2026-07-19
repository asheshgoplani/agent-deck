// The last VISIBLE session in a group must render └─ (IsLastInGroup=true) even
// when the group's trailing sessions are archived.
//
// GroupTree.Flatten() sets IsLastInGroup over a group's full session list —
// archived sessions included — so a trailing archived session gets the "last"
// flag. rebuildFlatItems then filters archived sessions out, leaving the last
// VISIBLE session with a stale IsLastInGroup=false, which renders ├─ instead of
// └─ (observed on the ops/home groups, 2026-07-18). rebuildFlatItems recomputes
// the flag on the final visible list to fix this.

package ui

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestIsLastInGroup_ArchivedSessionAtGroupEnd_LastVisibleRendersLast(t *testing.T) {
	home := NewHome()
	home.width, home.height = 120, 40
	home.initialLoading = false

	a1 := session.NewInstanceWithTool("a1", "/tmp/a1", "claude")
	a2 := session.NewInstanceWithTool("a2", "/tmp/a2", "claude")
	a3 := session.NewInstanceWithTool("a3", "/tmp/a3", "claude")
	for _, in := range []*session.Instance{a1, a2, a3} {
		in.GroupPath = "alpha"
		in.Status = session.StatusIdle
	}
	// a3 is the trailing session in the group AND archived — this is the one
	// Flatten marks IsLastInGroup, then rebuildFlatItems filters it away.
	a3.ArchivedAt = time.Now()

	instances := []*session.Instance{a1, a2, a3}
	home.instancesMu.Lock()
	home.instances = instances
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(instances)
	home.rebuildFlatItems()

	// Archived a3 must be filtered out of the visible list.
	if idx := sessionIndexByTitle(home, "a3"); idx >= 0 {
		t.Fatalf("archived session a3 should be filtered from the visible list, found at index %d", idx)
	}

	// a2 is now the last VISIBLE session in "alpha" → must be flagged last (└─).
	a2idx := sessionIndexByTitle(home, "a2")
	if a2idx < 0 {
		t.Fatalf("a2 not present in flatItems")
	}
	if !home.flatItems[a2idx].IsLastInGroup {
		t.Errorf("last visible session a2 has IsLastInGroup=false → renders ├─ instead of └─ " +
			"(stale flag from archived a3 at group end)")
	}

	// a1 is not last → must not be flagged.
	a1idx := sessionIndexByTitle(home, "a1")
	if a1idx < 0 {
		t.Fatalf("a1 not present in flatItems")
	}
	if home.flatItems[a1idx].IsLastInGroup {
		t.Errorf("non-last session a1 has IsLastInGroup=true")
	}
}

// View-mode partitioning (ActiveTop/PopulatedTop) can duplicate a group header
// and split one group's rows into separate top/bottom sections that share the
// same Path. Each section ends with its own └─, so the recompute must track
// last-row per header segment — not leak a later section's "seen" backward into
// an earlier one. Regression for the header-duplication case.
func TestIsLastInGroup_ViewModeSplit_EachSectionEndsWithLast(t *testing.T) {
	home := NewHome()
	home.width, home.height = 120, 40
	home.initialLoading = false

	c := session.NewInstanceWithTool("c-idle", "/tmp/c", "claude")
	a := session.NewInstanceWithTool("a-active", "/tmp/a", "claude")
	c.GroupPath, a.GroupPath = "alpha", "alpha"
	c.Status = session.StatusIdle
	// a is active (→ top section) AND last in the group's tree order, so Flatten
	// marks it IsLastInGroup=true; the recompute must not flip it to false just
	// because c (same Path) sits in the duplicated bottom section.
	a.Status = session.StatusRunning

	instances := []*session.Instance{c, a}
	home.instancesMu.Lock()
	home.instances = instances
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(instances)
	home.groupViewMode = session.GroupViewActiveTop
	home.rebuildFlatItems()

	// Sanity: the view mode actually split the list (a divider is present).
	if dividerIndex(home) < 0 {
		t.Fatalf("expected ActiveTop to split the group with a divider; flatItems=%d", len(home.flatItems))
	}

	aidx := sessionIndexByTitle(home, "a-active")
	if aidx < 0 {
		t.Fatalf("a-active not in flatItems")
	}
	if !home.flatItems[aidx].IsLastInGroup {
		t.Errorf("a-active is the last row of the top section but IsLastInGroup=false → renders ├─ " +
			"(header-duplication leaked the bottom section's flag backward)")
	}
	cidx := sessionIndexByTitle(home, "c-idle")
	if cidx < 0 || !home.flatItems[cidx].IsLastInGroup {
		t.Errorf("c-idle is the last row of the bottom section but IsLastInGroup=false")
	}
}
