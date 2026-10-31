package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestKimiSessionIdentitySurvivesLaterEventsAndInitialMessage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTDECK_INSTANCE_ID", "kimi-pending")
	t.Setenv("AGENTDECK_TOOL", "kimi")
	t.Setenv("AGENTDECK_HOOK_GENERATION", "current")
	if err := os.MkdirAll(getHooksDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	control, err := json.Marshal(hookGenerationControl{Generation: "current", InitialMessagePending: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(getHooksDir(), "kimi-pending.generation.json"), control, 0o600); err != nil {
		t.Fatal(err)
	}
	seed, err := json.Marshal(hookStatusFile{Status: "running", Event: "agentdeck_spawn_seed", HookGeneration: "current", InitialMessagePending: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(getHooksDir(), "kimi-pending.json"), seed, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, event := range []string{"SessionStart", "TurnStarted"} {
		payload, err := json.Marshal(map[string]any{
			"hook_event_name": event,
			"session_id":      "native-current",
			"client_type":     "kimi_code_cli",
		})
		if err != nil {
			t.Fatal(err)
		}
		runHookHandlerWith(t, string(payload))
	}

	var owned struct {
		ID string `json:"id"`
	}
	ownedData, err := os.ReadFile(filepath.Join(getHooksDir(), "kimi-pending.kimi-session"))
	if err != nil {
		t.Fatalf("SessionStart identity did not survive the later event: %v", err)
	}
	if err := json.Unmarshal(ownedData, &owned); err != nil {
		t.Fatal(err)
	}
	if owned.ID != "native-current" {
		t.Fatalf("owned session ID = %q, want native-current", owned.ID)
	}

	statusData, err := os.ReadFile(filepath.Join(getHooksDir(), "kimi-pending.json"))
	if err != nil {
		t.Fatal(err)
	}
	var status hookStatusFile
	if err := json.Unmarshal(statusData, &status); err != nil {
		t.Fatal(err)
	}
	if status.Status != "running" || !status.InitialMessagePending || status.ClientType != "kimi_code_cli" {
		t.Fatalf("TurnStarted status = %+v, want running Kimi status with initial message pending", status)
	}

	payload, err := json.Marshal(map[string]any{
		"hook_event_name": "Stop",
		"session_id":      "native-current",
		"client_type":     "kimi_code_cli",
	})
	if err != nil {
		t.Fatal(err)
	}
	runHookHandlerWith(t, string(payload))
	statusData, err = os.ReadFile(filepath.Join(getHooksDir(), "kimi-pending.json"))
	if err != nil {
		t.Fatal(err)
	}
	status = hookStatusFile{}
	if err := json.Unmarshal(statusData, &status); err != nil {
		t.Fatal(err)
	}
	if status.Status != "waiting" || status.InitialMessagePending {
		t.Fatalf("Stop status = %+v, want waiting with initial message completed", status)
	}

	t.Setenv("AGENTDECK_HOOK_GENERATION", "stale")
	payload, err = json.Marshal(map[string]any{
		"hook_event_name": "SessionStart",
		"session_id":      "native-stale",
		"client_type":     "kimi_code_cli",
	})
	if err != nil {
		t.Fatal(err)
	}
	runHookHandlerWith(t, string(payload))
	ownedData, err = os.ReadFile(filepath.Join(getHooksDir(), "kimi-pending.kimi-session"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(ownedData, &owned); err != nil {
		t.Fatal(err)
	}
	if owned.ID != "native-current" {
		t.Fatalf("stale generation replaced owned session ID with %q", owned.ID)
	}
}
