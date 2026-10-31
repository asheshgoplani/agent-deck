package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestKimiHookEventMapping(t *testing.T) {
	tests := []struct {
		event string
		want  string
	}{
		{event: "SessionStart", want: "waiting"},
		{event: "TurnStarted", want: "running"},
		{event: "UserPromptSubmit", want: "running"},
		{event: "PreToolUse", want: "running"},
		{event: "PostToolUse", want: "running"},
		{event: "PostToolUseFailure", want: "running"},
		{event: "Stop", want: "waiting"},
		{event: "Interrupt", want: "waiting"},
		{event: "PermissionRequest", want: "waiting"},
		{event: "StopFailure", want: "waiting"},
		{event: "SessionEnd", want: "dead"},
	}
	for _, tt := range tests {
		t.Run(tt.event, func(t *testing.T) {
			payload := hookPayload{HookEventName: tt.event, ClientType: "kimi_code_cli"}
			if got := mapHookEventToStatus(payload); got != tt.want {
				t.Fatalf("Kimi %s mapped to %q, want %q", tt.event, got, tt.want)
			}
		})
	}

	for _, event := range []string{"PostToolUse", "PostToolUseFailure"} {
		payload := hookPayload{HookEventName: event}
		if got := mapHookEventToStatus(payload); got != "waiting" {
			t.Errorf("non-Kimi %s changed to %q, want waiting", event, got)
		}
	}
}

func TestKimiHookHandlerWritesOnlyStatus(t *testing.T) {
	tests := []struct {
		event string
		want  string
	}{
		{event: "SessionStart", want: "waiting"},
		{event: "TurnStarted", want: "running"},
		{event: "UserPromptSubmit", want: "running"},
		{event: "PreToolUse", want: "running"},
		{event: "PostToolUse", want: "running"},
		{event: "PostToolUseFailure", want: "running"},
		{event: "Stop", want: "waiting"},
		{event: "Interrupt", want: "waiting"},
		{event: "PermissionRequest", want: "waiting"},
		{event: "StopFailure", want: "waiting"},
		{event: "SessionEnd", want: "dead"},
	}
	for _, tt := range tests {
		t.Run(tt.event, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("AGENTDECK_INSTANCE_ID", "kimi-instance")
			t.Setenv("AGENTDECK_TOOL", "kimi")
			t.Setenv("AGENTDECK_PRETOOL_SYNC", "1")
			t.Setenv("AGENTDECK_DSP_MODE", "1")
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			t.Setenv("CLAUDE_JOB_DIR", "")
			t.Cleanup(session.SetCommsLedgerForTest(true))

			payload, err := json.Marshal(map[string]any{
				"hook_event_name":        tt.event,
				"session_id":             "native-kimi",
				"client_type":            "kimi_code_cli",
				"prompt":                 "do not spool this prompt",
				"last_assistant_message": "do not spool this response",
			})
			if err != nil {
				t.Fatal(err)
			}
			stdout := captureStdout(t, func() { runHookHandlerWith(t, string(payload)) })
			if stdout != "" {
				t.Fatalf("Kimi hook wrote stdout %q", stdout)
			}

			data, err := os.ReadFile(filepath.Join(getHooksDir(), "kimi-instance.json"))
			if err != nil {
				t.Fatalf("read Kimi hook status: %v", err)
			}
			var got hookStatusFile
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if got.Status != tt.want || got.Event != tt.event || got.SessionID != "native-kimi" || got.ClientType != "kimi_code_cli" {
				t.Fatalf("Kimi hook status = %+v, want status %q event %q with native session and client type", got, tt.want, tt.event)
			}
			if got.DoneStatus != "" || got.TranscriptPath != "" {
				t.Fatalf("Kimi hook ran Claude Stop processing: %+v", got)
			}
			if entries, _ := session.ReadCommsSpool("kimi-instance"); len(entries) != 0 {
				t.Fatalf("Kimi hook spooled comms entries: %+v", entries)
			}
		})
	}
}

func TestHookPayloadDecodesKimiClientType(t *testing.T) {
	var payload hookPayload
	if err := json.Unmarshal([]byte(`{"hook_event_name":"SessionStart","session_id":"native-kimi","client_type":"kimi_code_cli"}`), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ClientType != "kimi_code_cli" {
		t.Fatalf("client_type = %q, want kimi_code_cli", payload.ClientType)
	}
}
