package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Issue #2582 (review blocker on #2696): Hermes' shell_hooks forwarder sends
// {hook_event_name, tool_name, tool_input, session_id, cwd, profile, extra},
// and a delegate_task child's events carry extra.platform == "subagent". The
// hook handler must record that platform in the status file so the session
// binding can ignore the child's turn edges.
func TestHookHandlerRecordsHermesExtraPlatform(t *testing.T) {
	cases := []struct {
		name, extra, want string
	}{
		{"subagent", `{"platform":"subagent","model":"m","parent_session_id":"x"}`, "subagent"},
		{"cli", `{"platform":"cli"}`, "cli"},
		{"no extra", ``, ""},
		{"non-string platform", `{"platform":7}`, ""},
		{"extra not an object", `"oops"`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			id := "hermes-platform-1"
			t.Setenv("AGENTDECK_INSTANCE_ID", id)
			t.Setenv("AGENTDECK_HOOK_GENERATION", "")
			payload := map[string]any{
				"hook_event_name": "pre_llm_call",
				"session_id":      "20261010_090000_d31e9a",
				"cwd":             t.TempDir(),
			}
			if tc.extra != "" {
				payload["extra"] = json.RawMessage(tc.extra)
			}
			b, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			runHookHandlerWithStdin(t, string(b))

			data, err := os.ReadFile(filepath.Join(getHooksDir(), id+".json"))
			if err != nil {
				t.Fatalf("status file not written: %v", err)
			}
			var got hookStatusFile
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if got.SessionID != "20261010_090000_d31e9a" || got.Event != "pre_llm_call" {
				t.Fatalf("status file = %+v", got)
			}
			if got.Platform != tc.want {
				t.Fatalf("platform = %q, want %q (file %s)", got.Platform, tc.want, data)
			}
		})
	}
}
