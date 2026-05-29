package session

// Issue #1225 / audit B1 — TRUE at-least-once durability.
//
// The drain stages its inbox records into a durable in-flight WAL (fsync'd)
// BEFORE removing the inbox file, and only marks turn_fingerprints consumed
// AFTER. A process death in the window between the inbox-remove (truncate) and
// the consumed-ledger save must RE-DELIVER the record on the next drain — the
// turn_fingerprint dedup then collapses any duplicate. It must never be lost.
//
// This is the regression for the audit's headline finding: two non-atomic disk
// ops (os.Remove under inboxWriteMu, saveConsumedTurnsLocked under
// consumedTurnsMu) meant a crash between them permanently dropped the record.

import (
	"testing"
)

// TestB1_DrainCrashBetweenTruncateAndLedgerSave_ReDelivers proves the at-least-
// once contract: a drain that died right after truncating the inbox but before
// finalizing the consumed ledger re-delivers the record on the next drain.
func TestB1_DrainCrashBetweenTruncateAndLedgerSave_ReDelivers(t *testing.T) {
	inboxTestHome(t)
	parent := "conductor-b1-1777100000"

	commitForStop(t, parent, "child-durable", "turn-1")

	// Simulate a drain that died right after truncating the inbox but before
	// finalizing the consumed ledger: run only the stage+truncate phase.
	staged, err := DrainStagePhaseForCrashTest(parent)
	if err != nil {
		t.Fatalf("stage phase: %v", err)
	}
	if len(staged) == 0 {
		t.Fatalf("stage phase must capture the pending record before truncate")
	}
	// The inbox file is gone (truncated)…
	if got := readInboxLines(t, parent); len(got) != 0 {
		t.Fatalf("inbox should be truncated after stage phase, still has %d", len(got))
	}

	// …and the consumed ledger was never written (crash). Simulate a fresh
	// process and drain again: the record MUST re-deliver, not vanish.
	ResetInboxFingerprintCacheForTest()
	got, err := DrainInboxForParent(parent)
	if err != nil {
		t.Fatalf("re-drain: %v", err)
	}
	found := false
	for _, ev := range got {
		if ev.ChildSessionID == "child-durable" {
			found = true
		}
	}
	if !found {
		t.Fatalf("B1 loss: record lost in crash window between truncate and ledger-save; re-drain saw %+v", got)
	}

	// Recovery is still exactly-once: a subsequent drain (consumed now durable)
	// must NOT re-deliver the same turn again.
	again, err := DrainInboxForParent(parent)
	if err != nil {
		t.Fatalf("third drain: %v", err)
	}
	for _, ev := range again {
		if ev.ChildSessionID == "child-durable" {
			t.Fatalf("recovery must be exactly-once: re-delivered %q twice", ev.ChildSessionID)
		}
	}
}
