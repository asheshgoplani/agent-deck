package main

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// An unattended sender must never answer a prompt addressed to a human.
//
// The 2026-08-20 orchestrate stall: a conductor raised an AskUserQuestion, its
// own 15-minute heartbeat nudged the session, and the composer guard read the
// rendered option list as an operator draft — Ctrl+C dismissed the question and
// the "restore" typed the options back as literal text. Twice in 45 minutes.
// The gate below is what makes that unwritable: a menu is refused, not sent
// into.
func TestNudgeGate_RefusesAwaitingChoice(t *testing.T) {
	gate := evaluateNudgeGate(true, "waiting", session.SubstateAwaitingChoice, false)

	if gate.Action != nudgeActionRefuse {
		t.Fatalf("action = %q, want %q — nudging a menu destroys it", gate.Action, nudgeActionRefuse)
	}
	if gate.Code != ErrCodeSessionAwaitingChoice {
		t.Errorf("code = %q, want %q", gate.Code, ErrCodeSessionAwaitingChoice)
	}
}

// awaiting-choice must NOT collapse into the stalled code. A supervisor loop
// treats stalled as "wedged, count a miss, eventually give up on the run", but
// a session waiting on a person is healthy and may wait for hours — the loop
// has to keep beating and escalate to that person instead.
func TestNudgeGate_AwaitingChoiceIsDistinctFromStalled(t *testing.T) {
	stalled := evaluateNudgeGate(true, "waiting", session.SubstateStalled, false)
	choice := evaluateNudgeGate(true, "waiting", session.SubstateAwaitingChoice, false)

	if stalled.Code == choice.Code {
		t.Fatalf("stalled and awaiting-choice share code %q; a supervisor cannot tell "+
			"'wedged' from 'waiting on a human'", stalled.Code)
	}
	if stalled.Code != ErrCodeSessionStalled {
		t.Errorf("stalled code regressed to %q", stalled.Code)
	}
}

// --force stays a real operator override: a human at a keyboard may decide to
// send anyway. The gate protects unattended callers, not deliberate ones.
func TestNudgeGate_ForceOverridesAwaitingChoice(t *testing.T) {
	gate := evaluateNudgeGate(true, "waiting", session.SubstateAwaitingChoice, true)
	if gate.Action != nudgeActionSend {
		t.Errorf("--force action = %q, want %q", gate.Action, nudgeActionSend)
	}
}

// An ordinary idle session is still nudgeable — the gate must not have made
// every waiting session unreachable.
func TestNudgeGate_StillSendsToIdlePrompt(t *testing.T) {
	gate := evaluateNudgeGate(true, "waiting", session.SubstateIdleAtEmptyPrompt, false)
	if gate.Action != nudgeActionSend {
		t.Errorf("idle-at-empty-prompt action = %q, want %q", gate.Action, nudgeActionSend)
	}
}
