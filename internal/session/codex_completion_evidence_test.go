package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCodexCompletionMatchesBoundSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		stateID   string
		boundID   string
		started   uint64
		completed uint64
		want      bool
	}{
		{name: "matching completion", stateID: "thread-1", boundID: "thread-1", started: 4, completed: 5, want: true},
		{name: "completion without observed start", stateID: "thread-1", boundID: "thread-1", completed: 1, want: true},
		{name: "newer start", stateID: "thread-1", boundID: "thread-1", started: 6, completed: 5},
		{name: "different session", stateID: "thread-2", boundID: "thread-1", started: 4, completed: 5},
		{name: "missing retained identity", boundID: "thread-1", started: 4, completed: 5},
		{name: "no completion", stateID: "thread-1", boundID: "thread-1", started: 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexCompletionMatchesBoundSession(tc.stateID, tc.boundID, tc.started, tc.completed); got != tc.want {
				t.Fatalf("codexCompletionMatchesBoundSession(%q, %q, %d, %d) = %v, want %v",
					tc.stateID, tc.boundID, tc.started, tc.completed, got, tc.want)
			}
		})
	}
}

func TestDebounceFlipFromRunningWithCodexCompletionSettlesFirstSample(t *testing.T) {
	t.Parallel()

	apply, pending, held := debounceFlipFromRunningWithCompletion(
		StatusRunning, StatusWaiting, "waiting", "waiting", false, true,
	)
	if held || pending || apply != StatusWaiting {
		t.Fatalf("matching completion must settle first sample; got apply=%s pending=%v held=%v", apply, pending, held)
	}

	apply, pending, held = debounceFlipFromRunningWithCompletion(
		StatusRunning, StatusWaiting, "waiting", "waiting", false, false,
	)
	if !held || !pending || apply != StatusRunning {
		t.Fatalf("pane-only first sample must remain debounced; got apply=%s pending=%v held=%v", apply, pending, held)
	}
}

func TestReadHookStatusFilePreservesRetainedGenerations(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_DATA_HOME", "")

	const instanceID = "instance-reader"
	hooksDir := GetHooksDir()
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	document := HookStateDocument{
		SchemaVersion:               HookStateSchemaV1,
		Status:                      "waiting",
		StateSessionID:              "thread-1",
		Event:                       "agent-turn-complete",
		Timestamp:                   1722190000,
		Generation:                  9,
		LastTurnStartedGeneration:   8,
		LastTurnCompletedGeneration: 9,
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, instanceID+".json"), data, 0o600); err != nil {
		t.Fatalf("write hook state: %v", err)
	}

	status := readHookStatusFile(instanceID)
	if status == nil {
		t.Fatal("readHookStatusFile returned nil")
	}
	if status.StateSessionID != "thread-1" || status.Generation != 9 ||
		status.LastTurnStartedGeneration != 8 || status.LastTurnCompletedGeneration != 9 {
		t.Fatalf("retained evidence lost: %#v", status)
	}
}

func TestInstanceRetainsMatchingCodexCompletionEvidence(t *testing.T) {
	t.Parallel()

	inst := &Instance{Tool: "codex", CodexSessionID: "thread-1"}
	inst.UpdateHookStatus(&HookStatus{
		Status:                      "waiting",
		SessionID:                   "thread-1",
		StateSessionID:              "thread-1",
		Event:                       "agent-turn-complete",
		UpdatedAt:                   time.Now(),
		Generation:                  5,
		LastTurnStartedGeneration:   4,
		LastTurnCompletedGeneration: 5,
	})

	inst.mu.RLock()
	defer inst.mu.RUnlock()
	if !inst.hasMatchingCodexCompletionLocked() {
		t.Fatal("matching retained completion was not available to status debounce")
	}
}
