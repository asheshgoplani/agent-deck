package session

// Issue #1225 Step 5 — removal regressions. When a child session is removed, all
// of its outbox artifacts must be swept so a reused id can't inherit stale state
// and ledgers can't leak: the per-parent inbox lines, the dead-letter record,
// the consumed-turn ledger entries, and (if the removed session was itself a
// parent) its Stop-hook block budget.

import (
	"os"
	"testing"
	"time"
)

func TestIssue1225_RemoveChild_SweepsAllOutboxArtifacts(t *testing.T) {
	inboxTestHome(t)
	parent := "conductor-rm-1777000400"
	child := "child-rm-1777000401"

	// 1. A pending inbox record for the child under its parent.
	if err := CommitToInbox(parent, TransitionNotificationEvent{
		ChildSessionID: child, ChildTitle: "worker", Profile: "personal",
		FromStatus: "running", ToStatus: "waiting", LastOutputHash: "h", Timestamp: time.Now(),
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// 2. A consumed-turn ledger entry for the child (drain records it).
	if _, err := DrainInboxForParent(parent); err != nil {
		t.Fatalf("drain: %v", err)
	}
	// 3. A dead-letter record for the child.
	dl := NewDeadLetterSink(t.TempDir() + "/missed.log")
	for i := 0; i < MaxUnresolvedAttempts; i++ {
		dl.RecordUnresolvable(TransitionNotificationEvent{ChildSessionID: child, Profile: "personal"})
	}
	if recs, _ := ReadDeadLetter(child); len(recs) == 0 {
		t.Fatalf("precondition: dead-letter record must exist")
	}
	// 4. A Stop-hook block-budget file keyed by the child (if it were a parent).
	saveStopBlockCountForTest(child, 2)

	// Re-commit so the inbox file exists again at removal time.
	if err := CommitToInbox(parent, TransitionNotificationEvent{
		ChildSessionID: child, ChildTitle: "worker", Profile: "personal",
		FromStatus: "running", ToStatus: "waiting", LastOutputHash: "h2", Timestamp: time.Now(),
	}); err != nil {
		t.Fatalf("recommit: %v", err)
	}

	// Remove the child — the single rm-time sweep must clean every artifact.
	if _, err := SweepInboxesForChildSession(child); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	// Inbox lines for the child are gone.
	if got := readInboxLines(t, parent); len(got) != 0 {
		t.Fatalf("inbox not swept: %d lines remain", len(got))
	}
	// Dead-letter record gone.
	if recs, _ := ReadDeadLetter(child); len(recs) != 0 {
		t.Fatalf("dead-letter not swept: %d records remain", len(recs))
	}
	// Consumed-turn ledger has no child@ entries.
	consumedTurnsMu.Lock()
	m := loadConsumedTurnsLocked(parent)
	consumedTurnsMu.Unlock()
	for fp := range m {
		if len(fp) >= len(child) && fp[:len(child)] == child {
			t.Fatalf("consumed-turn ledger not swept: %q remains", fp)
		}
	}
	// Stop-block budget file removed.
	if _, err := os.Stat(stopBlocksPathFor(child)); !os.IsNotExist(err) {
		t.Fatalf("stop-block budget not swept (stat err=%v)", err)
	}
}

// saveStopBlockCountForTest seeds a stop-block budget file for an instance.
func saveStopBlockCountForTest(instanceID string, count int) {
	stopBlockMu.Lock()
	defer stopBlockMu.Unlock()
	saveStopBlockCountLocked(instanceID, count)
}
