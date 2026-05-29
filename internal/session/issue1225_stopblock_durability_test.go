package session

// Audit B4 — a swallowed write error in the stop-block counter is the most
// dangerous failure in the design: if the MaxStopHookBlocks counter cannot be
// persisted, loadStopBlockCountLocked keeps reading 0, so every Stop would block
// (count 0 < cap) forever — the exact token-burn loop the guard exists to
// prevent. The fix reserves the block slot durably BEFORE draining, so a persist
// failure means: no block (no loop) AND no drain (the records stay in the inbox,
// not consumed-and-lost). This test forces the persist to fail and asserts both.
//
// Audit B12 — the fast path: a session with nothing pending (every leaf session)
// must return no-block WITHOUT touching the stop-block ledger at all.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestB4_StopBlockPersistFailure_NoLossNoLoop(t *testing.T) {
	inboxTestHome(t)
	parent := "conductor-b4-1777400000"

	commitForStop(t, parent, "child-protected", "turn-1")

	// Break the stop-block ledger writes: plant a regular FILE where the
	// stop-blocks directory needs to be, so MkdirAll inside the save fails.
	if err := os.MkdirAll(filepath.Dir(stopBlocksDir()), 0o755); err != nil {
		t.Fatalf("prep: %v", err)
	}
	if err := os.WriteFile(stopBlocksDir(), []byte("not-a-dir"), 0o644); err != nil {
		t.Fatalf("plant blocking file: %v", err)
	}

	dec, blocked, err := DrainForStopHook(parent, false)
	if err == nil {
		t.Fatalf("expected an error when the stop-block counter cannot persist")
	}
	if blocked || dec.Decision == "block" {
		t.Fatalf("fail-safe: must NOT block when the loop counter is unpersistable (would loop forever)")
	}

	// Remove the blocker; the record must still be there (not consumed/lost).
	if err := os.Remove(stopBlocksDir()); err != nil {
		t.Fatalf("unplant: %v", err)
	}
	pending, err := DrainInboxForParent(parent)
	if err != nil {
		t.Fatalf("recovery drain: %v", err)
	}
	found := false
	for _, ev := range pending {
		if ev.ChildSessionID == "child-protected" {
			found = true
		}
	}
	if !found {
		t.Fatalf("B4 loss: persist-failure consumed the record; saw %+v", pending)
	}
}

func TestB12_StopHook_LeafSessionNeverBlocksNorWritesLedger(t *testing.T) {
	inboxTestHome(t)
	leaf := "leaf-worker-1777400100" // no inbox ever committed to it

	dec, blocked, err := DrainForStopHook(leaf, false)
	if err != nil {
		t.Fatalf("leaf stop drain: %v", err)
	}
	if blocked || dec.Decision == "block" {
		t.Fatalf("leaf session must never block on Stop, got %+v", dec)
	}
	// And it must NOT have written a stop-block ledger file (no churn for the
	// global sync-flip on non-conductor sessions).
	if _, statErr := os.Stat(stopBlocksPathFor(leaf)); statErr == nil {
		t.Fatalf("leaf session wrote a stop-block ledger file; sync flip not scoped")
	}
}
