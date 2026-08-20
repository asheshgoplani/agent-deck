package ui

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func archivedAt() time.Time { return time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC) }

// #1987: the group header counted the raw session slice, which holds both
// archive partitions, while the deck renders exactly one. The reporter saw
// `demo (5)` above two rows, and `My Sessions (180)` above 19 reachable ones —
// 161 sessions that read as lost.
//
// The running/waiting tallies were wrong for a second reason the same filter
// fixes: archiving does not reset Status and the status updater skips archived
// sessions, so an archived session keeps contributing whatever it was doing
// when it was archived. A group holding an archived-while-running session
// reported a running session with no row and no process.
func TestBuildGroupRenderStats_CountsOnlyTheRenderedPartition(t *testing.T) {
	instances := []*session.Instance{
		{ID: "a1", Title: "active1", GroupPath: "demo", Status: session.StatusRunning},
		{ID: "z1", Title: "arch1", GroupPath: "demo", Status: session.StatusRunning, ArchivedAt: archivedAt()},
		{ID: "z2", Title: "arch2", GroupPath: "demo", Status: session.StatusWaiting, ArchivedAt: archivedAt()},
		{ID: "z3", Title: "arch3", GroupPath: "demo", Status: session.StatusRunning, ArchivedAt: archivedAt()},
		{ID: "a2", Title: "active2", GroupPath: "demo", Status: session.StatusWaiting},
	}
	h := &Home{groupTree: session.NewGroupTree(instances)}

	stats := h.buildGroupRenderStats(map[string]sessionRenderState{})
	got := stats["demo"]
	if got.sessionCount != 2 {
		t.Errorf("active view: sessionCount = %d, want 2 — the header must count the rows it heads (#1987)", got.sessionCount)
	}
	if got.running != 1 {
		t.Errorf("active view: running = %d, want 1 — an archived session keeps a live Status and has no process (#1987)", got.running)
	}
	if got.waiting != 1 {
		t.Errorf("active view: waiting = %d, want 1 (#1987)", got.waiting)
	}

	// The archived view (^) renders the other partition, so the header must
	// follow it there rather than excluding archived rows unconditionally.
	h.statusFilter = FilterModeArchived
	archived := h.buildGroupRenderStats(map[string]sessionRenderState{})["demo"]
	if archived.sessionCount != 3 {
		t.Errorf("archived view: sessionCount = %d, want 3 — in ^ the archived rows ARE the rows (#1987)", archived.sessionCount)
	}
	if archived.running != 2 || archived.waiting != 1 {
		t.Errorf("archived view: (running, waiting) = (%d, %d), want (2, 1)", archived.running, archived.waiting)
	}
}

// The live snapshot overrides the stored status, and the partition filter must
// apply to snapshot-backed rows too — otherwise the fix would hold only until
// the first render pass populated the cache.
func TestBuildGroupRenderStats_SnapshotRowsRespectThePartition(t *testing.T) {
	instances := []*session.Instance{
		{ID: "a1", Title: "active1", GroupPath: "demo", Status: session.StatusIdle},
		{ID: "z1", Title: "arch1", GroupPath: "demo", Status: session.StatusIdle, ArchivedAt: archivedAt()},
	}
	h := &Home{groupTree: session.NewGroupTree(instances)}

	snap := map[string]sessionRenderState{
		"a1": {status: session.StatusRunning},
		"z1": {status: session.StatusRunning},
	}
	got := h.buildGroupRenderStats(snap)["demo"]
	if got.running != 1 || got.sessionCount != 1 {
		t.Errorf("(running, sessionCount) = (%d, %d), want (1, 1) — a snapshot must not resurrect an archived row into the header (#1987)", got.running, got.sessionCount)
	}
}
