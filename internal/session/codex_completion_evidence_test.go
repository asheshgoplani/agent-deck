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
		name       string
		stateID    string
		boundID    string
		generation uint64
		started    uint64
		completed  uint64
		want       bool
	}{
		{name: "matching current completion", stateID: "thread-1", boundID: "thread-1", generation: 5, started: 4, completed: 5, want: true},
		{name: "completion without observed start", stateID: "thread-1", boundID: "thread-1", generation: 1, completed: 1, want: true},
		{name: "later event makes completion stale", stateID: "thread-1", boundID: "thread-1", generation: 6, started: 4, completed: 5},
		{name: "newer start", stateID: "thread-1", boundID: "thread-1", generation: 6, started: 6, completed: 5},
		{name: "different session", stateID: "thread-2", boundID: "thread-1", generation: 5, started: 4, completed: 5},
		{name: "missing retained identity", boundID: "thread-1", generation: 5, started: 4, completed: 5},
		{name: "no completion", stateID: "thread-1", boundID: "thread-1", generation: 4, started: 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexCompletionMatchesBoundSession(tc.stateID, tc.boundID, tc.generation, tc.started, tc.completed); got != tc.want {
				t.Fatalf("codexCompletionMatchesBoundSession(%q, %q, %d, %d, %d) = %v, want %v",
					tc.stateID, tc.boundID, tc.generation, tc.started, tc.completed, got, tc.want)
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

func TestCodexSubagentCompletionCannotReplaceMainCompletionEvidence(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)

	mainSID := uniqueSID(t)
	childSID := uniqueSID(t)
	seedCodexRolloutWithMeta(t, codexHome, mainSID, "user", "", false)
	seedCodexRolloutWithMeta(t, codexHome, childSID, "subagent", mainSID, true)

	inst.CodexSessionID = mainSID
	mainCompletionAt := time.Now()
	inst.UpdateHookStatus(&HookStatus{
		Status:                      "waiting",
		SessionID:                   mainSID,
		StateSessionID:              mainSID,
		Event:                       "main-turn-complete",
		UpdatedAt:                   mainCompletionAt,
		Generation:                  5,
		LastTurnStartedGeneration:   4,
		LastTurnCompletedGeneration: 5,
	})
	inst.UpdateHookStatus(&HookStatus{
		Status:                      "waiting",
		SessionID:                   childSID,
		StateSessionID:              childSID,
		Event:                       "agent-turn-complete",
		UpdatedAt:                   time.Now().Add(time.Second),
		Generation:                  6,
		LastTurnStartedGeneration:   4,
		LastTurnCompletedGeneration: 6,
	})

	inst.mu.RLock()
	defer inst.mu.RUnlock()
	if !inst.hasMatchingCodexCompletionLocked() {
		t.Fatal("rejected subagent completion replaced the main thread's retained completion evidence")
	}
	if inst.hookStatus != "waiting" || inst.hookEvent != "main-turn-complete" ||
		!inst.hookLastUpdate.Equal(mainCompletionAt) {
		t.Fatalf("rejected subagent completion replaced main hook status: status=%q event=%q updated=%v",
			inst.hookStatus, inst.hookEvent, inst.hookLastUpdate)
	}
}

func TestCodexCompatibleToolColdLoadsRetainedCompletionEvidence(t *testing.T) {
	skipIfNoTmuxBinary(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, "xdg-config"))
	t.Setenv("CODEX_HOME", filepath.Join(tmpHome, "codex"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	if err := SaveUserConfig(&UserConfig{Tools: map[string]ToolDef{
		"custom-codex": {Command: "codex", CompatibleWith: "codex"},
	}}); err != nil {
		t.Fatalf("save custom Codex tool: %v", err)
	}
	ClearUserConfigCache()

	inst := NewInstanceWithTool("custom-codex-cold-load", tmpHome, "custom-codex")
	if err := inst.tmuxSession.Start("sleep 3600"); err != nil {
		t.Fatalf("start tmux fixture: %v", err)
	}
	defer func() { _ = inst.tmuxSession.Kill() }()

	const threadID = "thread-custom-cold-load"
	inst.Status = StatusRunning
	inst.CodexSessionID = threadID
	if err := WriteHookState(inst.ID, HookStateEvent{
		Kind: HookTurnStarted, Status: "running", SessionID: threadID,
		Event: "turn.started", At: time.Now(),
	}); err != nil {
		t.Fatalf("write retained start: %v", err)
	}
	if err := WriteHookState(inst.ID, HookStateEvent{
		Kind: HookTurnCompleted, Status: "waiting", SessionID: threadID,
		Event: "turn.completed", At: time.Now(),
	}); err != nil {
		t.Fatalf("write retained completion: %v", err)
	}

	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	inst.mu.RLock()
	defer inst.mu.RUnlock()
	if inst.hookStateSessionID != threadID || inst.hookGeneration != 2 ||
		inst.hookLastTurnCompletedGeneration != 2 {
		t.Fatalf("Codex-compatible cold load lost retained evidence: state=%q generation=%d completed=%d",
			inst.hookStateSessionID, inst.hookGeneration, inst.hookLastTurnCompletedGeneration)
	}
}
