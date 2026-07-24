package main

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// ---------------------------------------------------------------------------
// `session nudge` precondition policy.
//
// This policy previously existed only as unreviewed bash inside whichever
// supervisor session happened to write it, and the version that shipped in the
// 2026-07-24 incident got it wrong in the most expensive way: it discarded the
// send result and reported every attempt as a success. These tests pin the
// three verdicts that make the failure unwritable.
// ---------------------------------------------------------------------------

func TestEvaluateNudgeGate_StalledIsRefusedNotSent(t *testing.T) {
	gate := evaluateNudgeGate(true, "waiting", session.SubstateStalled, false)

	if gate.Action != nudgeActionRefuse {
		t.Fatalf("a stalled session must be refused, got action %q", gate.Action)
	}
	if gate.Code != ErrCodeSessionStalled {
		t.Errorf("error code: want %q, got %q", ErrCodeSessionStalled, gate.Code)
	}
	// The reason has to name the recovery, or the supervisor reading it has no
	// next step and the session sits wedged — the actual failure being fixed.
	if !strings.Contains(gate.Reason, "Escape") {
		t.Errorf("reason must name the Escape+Enter recovery, got: %s", gate.Reason)
	}
}

func TestEvaluateNudgeGate_BusyIsSkippedNotFailed(t *testing.T) {
	for _, status := range []string{"running", "active", "starting"} {
		gate := evaluateNudgeGate(true, status, session.SubstateRunning, false)
		if gate.Action != nudgeActionSkip {
			t.Errorf("status %q: want skip, got %q", status, gate.Action)
		}
		// Must NOT carry an error code: a busy target is normal operation for
		// a polling watchdog. Making it an error trains supervisors to ignore
		// the exit code, which is precisely how the original bug survived.
		if gate.Code != "" {
			t.Errorf("status %q: busy must not be an error, got code %q", status, gate.Code)
		}
	}
}

func TestEvaluateNudgeGate_IdleSessionIsSent(t *testing.T) {
	for _, tc := range []struct {
		status   string
		substate session.Substate
	}{
		{"waiting", session.SubstateIdleAtEmptyPrompt},
		{"idle", session.SubstateIdleAtEmptyPrompt},
		{"waiting", session.SubstateNone},
	} {
		gate := evaluateNudgeGate(true, tc.status, tc.substate, false)
		if gate.Action != nudgeActionSend {
			t.Errorf("status %q/substate %q: want send, got %q", tc.status, tc.substate, gate.Action)
		}
	}
}

func TestEvaluateNudgeGate_NotRunningIsRefusedEvenWithForce(t *testing.T) {
	for _, force := range []bool{false, true} {
		gate := evaluateNudgeGate(false, "stopped", session.SubstateNone, force)
		if gate.Action != nudgeActionRefuse {
			t.Errorf("force=%v: a dead session must be refused, got %q", force, gate.Action)
		}
		if gate.Code != ErrCodeInvalidOperation {
			t.Errorf("force=%v: want %q, got %q", force, ErrCodeInvalidOperation, gate.Code)
		}
	}
}

// TestEvaluateNudgeGate_ForceOverridesStalledAndBusy: --force is a deliberate
// operator override, so it must bypass the advisory gates (but not the
// impossible one, covered above).
func TestEvaluateNudgeGate_ForceOverridesStalledAndBusy(t *testing.T) {
	if gate := evaluateNudgeGate(true, "waiting", session.SubstateStalled, true); gate.Action != nudgeActionSend {
		t.Errorf("--force must override stalled, got %q", gate.Action)
	}
	if gate := evaluateNudgeGate(true, "running", session.SubstateRunning, true); gate.Action != nudgeActionSend {
		t.Errorf("--force must override busy, got %q", gate.Action)
	}
}

func TestIsBusyForNudge(t *testing.T) {
	busy := []string{"running", "active", "starting", "RUNNING"}
	notBusy := []string{"waiting", "idle", "error", "stopped", "queued", ""}

	for _, s := range busy {
		if !isBusyForNudge(s) {
			t.Errorf("%q should be busy", s)
		}
	}
	for _, s := range notBusy {
		if isBusyForNudge(s) {
			t.Errorf("%q should not be busy", s)
		}
	}
}
