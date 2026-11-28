package session

import (
	"testing"
	"time"
)

// PR #3166 review F1: a worker that reports to a launcher depends on it like a
// tree child does. A cross-harness switch archives the source and gives the
// replacement a new id, so the switch snapshot must count reports_to workers
// as dependents and refuse.
func TestIssue3165_SwitchSnapshotCountsReportsToWorkersAsDependents(t *testing.T) {
	_, coord := issue3165Fleet()
	worker := issue3165Worker("wrk-3165-sw", "", coord.ID) // top-level, no tree parent
	snap := SnapshotSwitchSource(coord, []*Instance{coord, worker}, false, false)
	if !snap.HasChildren {
		t.Fatalf("launcher with a reports_to worker: HasChildren=false, want true so the switch is refused")
	}
	// A session whose own reports_to names itself is not its own dependent.
	self := issue3165Worker("wrk-3165-self", "", "")
	self.ReportsTo = self.ID
	if snap := SnapshotSwitchSource(self, []*Instance{self}, false, false); snap.HasChildren {
		t.Fatalf("self reports_to counted as a dependent")
	}
}

// PR #3166 review F2: an archived launcher is still in the registry, but a
// nested worker's records must fall back to its live tree parent as they do
// for a removed launcher, so the root gets them again (as before #3165).
func TestIssue3165_ArchivedLauncherFallsBackToTheLiveTreeParent(t *testing.T) {
	root, coord := issue3165Fleet()
	coord.SetArchivedAtThreadSafe(time.Now())
	worker := issue3165Worker("wrk-3165-arch", root.ID, coord.ID)
	byID := map[string]*Instance{root.ID: root, coord.ID: coord, worker.ID: worker}

	if got := statsParentForIn(worker, byID); got != root.ID {
		t.Errorf("statsParentForIn = %q, want the root %q", got, root.ID)
	}
	if got := parentOf(worker, byID); got != root.ID {
		t.Errorf("parentOf = %q, want the root %q", got, root.ID)
	}

	profile := "_test-3165-archived-launcher"
	seedNotifyInstances(t, profile, root, coord, worker)
	n := NewTransitionNotifier()
	t.Cleanup(n.Close)
	res := n.NotifyFinished(TransitionNotificationEvent{
		ChildSessionID: worker.ID, ChildTitle: worker.Title, Profile: profile,
		DoneStatus: "ok", DoneSummary: "done", Timestamp: time.Now(),
	})
	coordN, rootN := len(readInboxLines(t, coord.ID)), len(readInboxLines(t, root.ID))
	if rootN != 1 || coordN != 0 {
		t.Fatalf("delivery=%q reason=%q coordInbox=%d rootInbox=%d, want the root inbox only",
			res.DeliveryResult, res.DeadLetterReason, coordN, rootN)
	}
}

// An archived launcher with no live tree parent keeps the launcher id: the
// top-level worker's record waits in the launcher's inbox, as a child of an
// archived tree parent's does. An archived tree parent is no fallback.
func TestIssue3165_ArchivedLauncherWithoutLiveParentKeepsTheLauncher(t *testing.T) {
	root, coord := issue3165Fleet()
	coord.SetArchivedAtThreadSafe(time.Now())
	top := issue3165Worker("wrk-3165-top", "", coord.ID)
	nested := issue3165Worker("wrk-3165-nest", root.ID, coord.ID)
	root.SetArchivedAtThreadSafe(time.Now())
	byID := map[string]*Instance{root.ID: root, coord.ID: coord, top.ID: top, nested.ID: nested}
	if got := top.reportTargetIDIn(byID); got != coord.ID {
		t.Errorf("top-level worker: %q, want the launcher id %q", got, coord.ID)
	}
	if got := nested.reportTargetIDIn(byID); got != coord.ID {
		t.Errorf("nested worker, tree parent archived too: %q, want the launcher id %q", got, coord.ID)
	}
	// A plain child of an archived parent (no reports_to) is unchanged.
	child := &Instance{ID: "plain-child", ParentSessionID: root.ID}
	if got := child.reportTargetIDIn(byID); got != root.ID {
		t.Errorf("plain child of an archived parent: %q, want %q", got, root.ID)
	}
}
