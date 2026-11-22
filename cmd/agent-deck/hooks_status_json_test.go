package main

import (
	"encoding/json"
	"testing"
)

// Every `<kind>-hooks status --json` used to ignore --json and print text.
func TestHooksStatusJSON_AllKindsEmitOneShape(t *testing.T) {
	cases := []struct {
		kind string
		run  func([]string)
	}{
		{"claude", handleHooks},
		{"codex", handleCodexHooks},
		{"gemini", handleGeminiHooks},
		{"hermes", handleHermesHooks},
		{"cursor", handleCursorHooks},
		{"pi", handlePiHooks},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", home+"/config")
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			t.Setenv("CODEX_HOME", "")
			out := captureStdout(t, func() { tc.run([]string{"status", "--json"}) })
			var got hooksStatusJSON
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("%s status --json is not JSON: %v\n%s", tc.kind, err, out)
			}
			if got.Kind != tc.kind {
				t.Fatalf("kind = %q, want %q", got.Kind, tc.kind)
			}
			if got.State != "not-installed" {
				t.Fatalf("state = %q, want not-installed on an empty home\n%s", got.State, out)
			}
		})
	}
}

func TestHooksStatusJSON_TextModeUnchanged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	out := captureStdout(t, func() { handleGeminiHooks([]string{"status"}) })
	if json.Valid([]byte(out)) {
		t.Fatalf("plain status must stay text, got %q", out)
	}
}
