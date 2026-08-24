package session

import (
	"testing"
)

// A terminal observation without a completion sentinel is a transition, not a
// finished task.  Exercise the same producer/export/collapse chain used by
// remote drain so a later synthetic ledger record cannot hide the stall.
func TestIssue1952_OrdinaryWaitingTurnRemainsActionableAfterRemoteDrain(t *testing.T) {
	profile := "_test-1952-ordinary-stall"
	storage := reviewTestHome(t, profile)
	child := parentlessWorker("remote-quota-stall", "quota-stalled worker")
	if err := storage.SaveWithGroups([]*Instance{child}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	d := fieldDaemon(t)
	d.recordTerminalTurns(profile,
		map[string]*Instance{child.ID: child},
		map[string]string{child.ID: string(StatusWaiting)}, nil)

	records, err := ExportPendingRecords()
	if err != nil {
		t.Fatalf("ExportPendingRecords: %v", err)
	}
	collapsed := collapseLastWins(records)
	if len(collapsed) != 1 {
		t.Fatalf("one observed turn must remain one record after export and collapse, got %+v", collapsed)
	}
	got := collapsed[0]
	if got.Kind == transitionKindFinished || got.FromStatus != string(StatusRunning) || got.ToStatus != string(StatusWaiting) {
		t.Fatalf("ordinary waiting turn was converted into a completion instead of retaining its actionable transition: %+v", got)
	}
}

// A registry row can precede its tmux session.  Rejecting that first poll must
// not poison the turn dedup cache: the unchanged observation is eligible once
// the session becomes live.
func TestIssue1952_TerminalTurnRetriesAfterLivenessRecovery(t *testing.T) {
	profile := "_test-1952-liveness-recovery"
	storage := reviewTestHome(t, profile)
	child := parentlessWorker("late-live-worker", "late live worker")
	if err := storage.SaveWithGroups([]*Instance{child}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	live := false
	d := fieldDaemon(t)
	d.turnLiveCheck = func(*Instance) bool { return live }
	byID := map[string]*Instance{child.ID: child}
	statuses := map[string]string{child.ID: string(StatusWaiting)}
	d.recordTerminalTurns(profile, byID, statuses, nil)
	live = true
	d.recordTerminalTurns(profile, byID, statuses, nil)

	records, err := ExportPendingRecords()
	if err != nil {
		t.Fatalf("ExportPendingRecords: %v", err)
	}
	if len(records) != 1 || records[0].FromStatus != string(StatusRunning) || records[0].ToStatus != string(StatusWaiting) {
		t.Fatalf("unchanged turn must be recorded after liveness recovers, got %+v", records)
	}
}
