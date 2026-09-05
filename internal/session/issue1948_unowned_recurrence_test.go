package session

// Issue #1948, review P1 — finishing the finding.
//
// P1's fix persists a parentless session's transitions in the drainable
// _unowned ledger. That makes the FIRST stall drainable. It does not, on its
// own, make the SECOND one drainable, and a quota stall is a recurring
// condition: a session stalls, the conductor notices, the quota resets, and it
// stalls again an hour later.
//
// The _unowned ledger is the one inbox file with NO consumer — ExportPendingRecords
// is read-only by contract and only SweepInboxByTTL removes anything — so the
// first record stays PENDING and pins the dedup fingerprints for the whole TTL
// window. Verified on the loopback harness: a real parked-at-waiting session
// emits its flip with an EMPTY LastOutputHash, which collapses EventFingerprint
// to child|from|to. Without unownedTurnSignal the second stall is dropped on the
// producing host and the drain has nothing to return — the reported failure,
// one layer below the fix for it.

import (
	"testing"
	"time"
)

// stallEvent is one quota-stall flip with NO derivable turn signal — the shape
// transitionEventOutputHash actually produces for a parked session.
func stallEvent(child, profile string, at time.Time) TransitionNotificationEvent {
	return TransitionNotificationEvent{
		ChildSessionID: child,
		ChildTitle:     "remote-stall",
		Profile:        profile,
		FromStatus:     "running",
		ToStatus:       "waiting",
		Substate:       "usage-limit",
		LastOutputHash: "", // the case this test exists for
		Timestamp:      at,
	}
}

func TestIssue1948P1_RecurringStallIsDrainableAgain(t *testing.T) {
	profile := "_test-1948-p1-recur"
	storage := reviewTestHome(t, profile)

	child := parentlessWorker("worker-b-recur", "remote-stall")
	if err := storage.SaveWithGroups([]*Instance{child}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	first := time.Now().Add(-2 * time.Hour)
	if _, err := recordUnownedTransition(stallEvent(child.ID, profile, first)); err != nil {
		t.Fatalf("record first stall: %v", err)
	}
	// A retry of the SAME stamped event must still collapse (issue #824): the
	// notifier stamps an event once and reuses it across retries.
	if isNew, err := recordUnownedTransition(stallEvent(child.ID, profile, first)); err != nil {
		t.Fatalf("record retry: %v", err)
	} else if isNew {
		t.Fatalf("a retry of one stamped event must collapse, not become a second record")
	}

	// The quota resets, the session runs, and it stalls AGAIN. This is a new
	// logical event and the conductor has to see it.
	second := first.Add(90 * time.Minute)
	if isNew, err := recordUnownedTransition(stallEvent(child.ID, profile, second)); err != nil {
		t.Fatalf("record second stall: %v", err)
	} else if !isNew {
		t.Fatalf("a stall that recurs after the first was drained must be recorded again, " +
			"not collapsed onto the still-pending first record")
	}

	records, err := ExportPendingRecords()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	var stalls []TransitionNotificationEvent
	for _, r := range records {
		if r.ChildSessionID == child.ID {
			stalls = append(stalls, r)
		}
	}
	if len(stalls) != 2 {
		t.Fatalf("both stalls must be drainable, got %d: %+v", len(stalls), stalls)
	}
	if EventFingerprint(stalls[0]) == EventFingerprint(stalls[1]) {
		t.Fatalf("two genuinely distinct stalls must not share an EventFingerprint: %+v", stalls)
	}
	// The consumer-side exactly-once ledger (#1225) keys on TurnFingerprint, so
	// distinctness has to survive there too or the record arrives and is
	// swallowed by the conductor instead of by the producer.
	if stalls[0].TurnFingerprint == stalls[1].TurnFingerprint {
		t.Fatalf("two distinct stalls must not share a TurnFingerprint, or the "+
			"conductor's #1225 ledger collapses the second: %+v", stalls)
	}
}

// A completion must be left strictly alone: its _unowned copy and its completion-
// ledger copy are two views of ONE event that the export unions and dedupes by
// EventFingerprint. Synthesizing a per-emit signal for it would make them hash
// differently and re-open review P2b.
func TestIssue1948P1_UnownedCompletionKeepsP2bDedup(t *testing.T) {
	profile := "_test-1948-p1-done"
	reviewTestHome(t, profile)

	done := TransitionNotificationEvent{
		ChildSessionID: "worker-b-done",
		ChildTitle:     "remote-migrate",
		Profile:        profile,
		Kind:           transitionKindFinished,
		DoneStatus:     "ok",
		DoneSummary:    "migration finished",
		Timestamp:      time.Now(),
	}
	if got := unownedTurnSignal(done); got != "" {
		t.Fatalf("a completion must keep an empty turn signal so the ledger and inbox "+
			"copies of one completion still dedupe (review P2b), got %q", got)
	}

	// The same completion as the ledger would synthesize it, stamped later by the
	// other producer, must still share a fingerprint.
	ledgerCopy := done
	ledgerCopy.Timestamp = done.Timestamp.Add(90 * time.Second)
	ledgerCopy.LastOutputHash = unownedTurnSignal(ledgerCopy)
	inboxCopy := done
	inboxCopy.LastOutputHash = unownedTurnSignal(inboxCopy)
	if EventFingerprint(ledgerCopy) != EventFingerprint(inboxCopy) {
		t.Fatalf("the two production copies of one completion must still dedupe (P2b)")
	}
}
