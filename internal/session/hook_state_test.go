package session

import (
	"encoding/json"
	"testing"
	"time"
)

func TestHookStateDocumentReadsLegacyStatusJSON(t *testing.T) {
	t.Parallel()

	legacy := []byte(`{"status":"waiting","session_id":"thread-1","event":"agent-turn-complete","ts":1722186000}`)
	var state HookStateDocument
	if err := json.Unmarshal(legacy, &state); err != nil {
		t.Fatalf("Unmarshal legacy status: %v", err)
	}
	if state.Status != "waiting" || state.SessionID != "thread-1" || state.Event != "agent-turn-complete" {
		t.Fatalf("legacy fields changed: %#v", state)
	}
	if state.Generation != 0 || state.LastTurnStartedGeneration != 0 || state.LastTurnCompletedGeneration != 0 {
		t.Fatalf("legacy document must start without retained generations: %#v", state)
	}
}

func TestAdvanceHookStateRetainsSameSecondStartAndCompletion(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 7, 28, 17, 50, 0, 0, time.UTC)
	state, err := AdvanceHookState(HookStateDocument{}, HookStateEvent{
		Kind: HookTurnStarted, Status: "running", SessionID: "thread-1", Event: "turn.started", At: at,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	state, err = AdvanceHookState(state, HookStateEvent{
		Kind: HookTurnCompleted, Status: "waiting", SessionID: "thread-1", Event: "agent-turn-complete", At: at,
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	if state.Generation != 2 || state.LastTurnStartedGeneration != 1 || state.LastTurnCompletedGeneration != 2 {
		t.Fatalf("retained generation order lost: %#v", state)
	}
	if state.LastTurnStartedAt != at.UnixNano() || state.LastTurnCompletedAt != at.UnixNano() {
		t.Fatalf("event times not retained: %#v", state)
	}
}

func TestAdvanceHookStateNewSessionCannotReusePriorCompletion(t *testing.T) {
	t.Parallel()

	state := HookStateDocument{
		SchemaVersion:               HookStateSchemaV1,
		Generation:                  8,
		SessionID:                   "thread-old",
		StateSessionID:              "thread-old",
		LastTurnStartedGeneration:   7,
		LastTurnCompletedGeneration: 8,
	}
	next, err := AdvanceHookState(state, HookStateEvent{
		Kind: HookTurnStarted, Status: "running", SessionID: "thread-new", Event: "turn.started",
	})
	if err != nil {
		t.Fatalf("AdvanceHookState: %v", err)
	}
	if next.Generation != 9 || next.LastTurnStartedGeneration != 9 || next.LastTurnCompletedGeneration != 0 {
		t.Fatalf("new session inherited prior completion: %#v", next)
	}
}

func TestAdvanceHookStateDeadEventClearsRetainedEvidence(t *testing.T) {
	t.Parallel()

	state := HookStateDocument{
		SchemaVersion:               HookStateSchemaV1,
		Generation:                  5,
		SessionID:                   "thread-1",
		StateSessionID:              "thread-1",
		LastTurnStartedGeneration:   4,
		LastTurnCompletedGeneration: 5,
		LastTurnStartedAt:           100,
		LastTurnCompletedAt:         200,
	}
	next, err := AdvanceHookState(state, HookStateEvent{
		Kind: HookStatusOnly, Status: "dead", Event: "session.dead",
	})
	if err != nil {
		t.Fatalf("AdvanceHookState: %v", err)
	}
	if next.StateSessionID != "" || next.LastTurnStartedGeneration != 0 ||
		next.LastTurnCompletedGeneration != 0 || next.LastTurnStartedAt != 0 ||
		next.LastTurnCompletedAt != 0 {
		t.Fatalf("dead event did not clear retained evidence: %#v", next)
	}
}

func TestValidateHookInstanceIDRejectsUnsafePaths(t *testing.T) {
	t.Parallel()

	for _, valid := range []string{"instance-001", "550e8400-e29b-41d4-a716-446655440000", "abc_DEF"} {
		if err := validateHookInstanceID(valid); err != nil {
			t.Errorf("valid ID %q rejected: %v", valid, err)
		}
	}
	for _, invalid := range []string{"", ".", "..", "../victim", "nested/victim", `nested\victim`, "has space", "💣"} {
		if err := validateHookInstanceID(invalid); err == nil {
			t.Errorf("unsafe ID %q accepted", invalid)
		}
	}
}
