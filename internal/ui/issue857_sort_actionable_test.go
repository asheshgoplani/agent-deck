package ui

// Regression coverage for #857 — sort sessions by most recently actionable
// within a group. The reporter's pain: 2-3 active sessions get buried among
// 7+ parked ones because the in-group order is fixed/creation-order. The fix
// reorders within each group so the most "ready for me" sessions surface to
// the top.
//
// Priority (lower = more actionable, surfaces higher):
//   error    : something broke, surface first
//   waiting  : model done, awaiting user input
//   running  : model actively working
//   idle     : nothing to do
//   stopped  : user-parked, bottom of pile
//
// Tie-break within the same status: LastAccessedAt desc (recent attention
// first), then persisted Order asc as the stable third key so user-customized
// position still survives when statuses match (TestSessionOrderPersistence,
// TestSessionOrderMigration).

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestSessionList_SortByActionable_RegressionFor857 sets up five sessions in
// one group — one each of running/waiting/error/idle/stopped — and asserts
// the rendered order is the actionable order, not the persisted Order. The
// `Order` values intentionally invert the desired output so a regression to
// the old Order-based sort flips at least four positions.
func TestSessionList_SortByActionable_RegressionFor857(t *testing.T) {
	now := time.Now()

	// Order values are reversed vs. the desired actionable order; the
	// pre-fix code sorted by Order asc and would produce
	// [running, waiting, error, idle, stopped].
	instances := []*session.Instance{
		{ID: "run", Title: "running-sess", GroupPath: "g", Order: 1, Status: session.StatusRunning, LastAccessedAt: now.Add(-1 * time.Minute)},
		{ID: "wait", Title: "waiting-sess", GroupPath: "g", Order: 2, Status: session.StatusWaiting, LastAccessedAt: now.Add(-2 * time.Minute)},
		{ID: "err", Title: "error-sess", GroupPath: "g", Order: 3, Status: session.StatusError, LastAccessedAt: now.Add(-3 * time.Minute)},
		{ID: "idle", Title: "idle-sess", GroupPath: "g", Order: 4, Status: session.StatusIdle, LastAccessedAt: now.Add(-4 * time.Minute)},
		{ID: "stop", Title: "stopped-sess", GroupPath: "g", Order: 5, Status: session.StatusStopped, LastAccessedAt: now.Add(-5 * time.Minute)},
	}

	tree := session.NewGroupTree(instances)
	group := tree.Groups["g"]
	if group == nil {
		t.Fatalf("group %q not in tree", "g")
	}
	if len(group.Sessions) != 5 {
		t.Fatalf("expected 5 sessions in group, got %d", len(group.Sessions))
	}

	wantIDs := []string{"err", "wait", "run", "idle", "stop"}
	gotIDs := make([]string, len(group.Sessions))
	for i, s := range group.Sessions {
		gotIDs[i] = s.ID
	}

	for i, want := range wantIDs {
		if gotIDs[i] != want {
			t.Errorf("position %d: want %q (status %s), got %q\n  full order: got=%v want=%v",
				i, want, statusForID(instances, want), gotIDs[i], gotIDs, wantIDs)
		}
	}
}

// TestSessionList_SortByActionable_TimestampTieBreak verifies the secondary
// sort: when two sessions share the same status, the one accessed more
// recently surfaces first. Mirrors the issue's "ready for me" intuition —
// the actionable session you just left should stay above one you parked
// hours ago.
func TestSessionList_SortByActionable_TimestampTieBreak(t *testing.T) {
	now := time.Now()

	instances := []*session.Instance{
		{ID: "old-wait", Title: "old", GroupPath: "g", Order: 1, Status: session.StatusWaiting, LastAccessedAt: now.Add(-3 * time.Hour)},
		{ID: "new-wait", Title: "new", GroupPath: "g", Order: 2, Status: session.StatusWaiting, LastAccessedAt: now.Add(-5 * time.Minute)},
		{ID: "old-run", Title: "old-r", GroupPath: "g", Order: 3, Status: session.StatusRunning, LastAccessedAt: now.Add(-1 * time.Hour)},
		{ID: "new-run", Title: "new-r", GroupPath: "g", Order: 4, Status: session.StatusRunning, LastAccessedAt: now.Add(-1 * time.Minute)},
	}

	tree := session.NewGroupTree(instances)
	group := tree.Groups["g"]
	if group == nil {
		t.Fatalf("group %q not in tree", "g")
	}

	// Waiting (priority 1) ranks above running (priority 2). Within each
	// status bucket the recently-accessed session ranks first.
	want := []string{"new-wait", "old-wait", "new-run", "old-run"}
	got := make([]string, len(group.Sessions))
	for i, s := range group.Sessions {
		got[i] = s.ID
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d: want %q, got %q (full: got=%v want=%v)", i, want[i], got[i], got, want)
		}
	}
}

func statusForID(insts []*session.Instance, id string) session.Status {
	for _, i := range insts {
		if i.ID == id {
			return i.Status
		}
	}
	return ""
}
