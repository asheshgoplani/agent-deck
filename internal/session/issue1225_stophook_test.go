package session

// Issue #1225 Step 3 — the busy-parent fix. When a conductor finishes its turn
// the Stop hook drains the durable outbox and returns {decision:"block",reason}
// so the completions become the conductor's next turn input — at the moment it
// is provably free. A stop_hook_active + max-consecutive-blocks guard prevents
// an infinite Stop→block loop (the Agent Teams #47930 token-burn failure).

import (
	"strings"
	"testing"
	"time"
)

func commitForStop(t *testing.T, parent, child, turn string) {
	t.Helper()
	if err := CommitToInbox(parent, TransitionNotificationEvent{
		ChildSessionID: child, ChildTitle: "worker", Profile: "personal",
		FromStatus: "running", ToStatus: "waiting",
		LastOutputHash: turn, Timestamp: time.Now(),
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// Case 1 (busy-parent proof, unit form): a completion committed while the
// parent was busy is injected at the parent's next turn boundary, exactly once.
func TestIssue1225_StopHook_BusyParentReceivesAtTurnBoundaryExactlyOnce(t *testing.T) {
	inboxTestHome(t)
	parent := "conductor-stop-1777000300"

	// Child finished while parent was mid-turn → durable record only.
	commitForStop(t, parent, "child-busy", "turn-1")

	// Parent's turn ends: Stop hook drains and blocks with the completion.
	dec, blocked, err := DrainForStopHook(parent, false)
	if err != nil {
		t.Fatalf("stop drain: %v", err)
	}
	if !blocked || dec.Decision != "block" {
		t.Fatalf("busy-parent proof: expected decision=block at turn boundary, got %+v", dec)
	}
	if !strings.Contains(dec.Reason, "child-busy") {
		t.Fatalf("injected reason missing completion: %q", dec.Reason)
	}

	// Next turn boundary: nothing pending → no block (exactly once).
	dec2, blocked2, err := DrainForStopHook(parent, true)
	if err != nil {
		t.Fatalf("second stop drain: %v", err)
	}
	if blocked2 || dec2.Decision != "" {
		t.Fatalf("exactly-once: second turn boundary must not block, got %+v", dec2)
	}
}

// Case 9 (max-blocks loop guard): with a child that keeps finishing a NEW turn
// every cycle, the Stop hook can block at most MaxStopHookBlocks times in a row;
// after that it stops blocking (lets the conductor reach idle) even though
// records are pending. A fresh user turn (stop_hook_active=false) resets it.
func TestIssue1225_StopHook_MaxBlocksGuardCannotLoopForever(t *testing.T) {
	inboxTestHome(t)
	parent := "conductor-stop-1777000310"

	blocks := 0
	// Simulate continuous child activity: a new turn each Stop cycle. The first
	// Stop is a genuine turn boundary (stop_hook_active=false); the rest are
	// stop-hook-induced continuations (stop_hook_active=true).
	for i := 0; i < 10; i++ {
		commitForStop(t, parent, "child-chatty", "turn-"+string(rune('A'+i)))
		_, blocked, err := DrainForStopHook(parent, i != 0)
		if err != nil {
			t.Fatalf("drain %d: %v", i, err)
		}
		if blocked {
			blocks++
		}
	}

	if blocks == 0 {
		t.Fatalf("guard too strict: never blocked")
	}
	if blocks > MaxStopHookBlocks {
		t.Fatalf("loop guard failed: blocked %d times in a row, cap is %d", blocks, MaxStopHookBlocks)
	}

	// A fresh user turn resets the budget: a newly pending completion blocks again.
	commitForStop(t, parent, "child-after-reset", "turn-Z")
	_, blocked, err := DrainForStopHook(parent, false)
	if err != nil {
		t.Fatalf("post-reset drain: %v", err)
	}
	if !blocked {
		t.Fatalf("stop_hook_active=false must reset the block budget and allow a new block")
	}
}

// Guard must NOT consume (lose) records when it trips: records pending when the
// budget is exhausted remain in the inbox for the heartbeat to drain.
func TestIssue1225_StopHook_GuardTripDoesNotLoseRecords(t *testing.T) {
	inboxTestHome(t)
	parent := "conductor-stop-1777000320"

	// Exhaust the block budget with continuation stops.
	for i := 0; i < MaxStopHookBlocks; i++ {
		commitForStop(t, parent, "c"+string(rune('0'+i)), "t"+string(rune('0'+i)))
		if _, _, err := DrainForStopHook(parent, i != 0); err != nil {
			t.Fatalf("warm drain %d: %v", i, err)
		}
	}
	// Budget now exhausted. A new completion arrives; the next continuation Stop
	// must NOT block, and must NOT consume the record.
	commitForStop(t, parent, "child-preserved", "turn-preserve")
	_, blocked, err := DrainForStopHook(parent, true)
	if err != nil {
		t.Fatalf("guarded drain: %v", err)
	}
	if blocked {
		t.Fatalf("budget exhausted: must not block")
	}
	// The record must still be drainable by the heartbeat path.
	pending, err := DrainInboxForParent(parent)
	if err != nil {
		t.Fatalf("heartbeat drain: %v", err)
	}
	found := false
	for _, ev := range pending {
		if ev.ChildSessionID == "child-preserved" {
			found = true
		}
	}
	if !found {
		t.Fatalf("guard trip lost the pending record; heartbeat saw: %+v", pending)
	}
}
