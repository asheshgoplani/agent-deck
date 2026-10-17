package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSessionShowJSONReportsOpenCodeIdentity(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runAgentDeck(t, home, "add", "-t", "opencode-show", "-c", "opencode", "--no-parent", "--json", project)
	if code != 0 {
		t.Fatalf("add failed (%d): %s\n%s", code, stdout, stderr)
	}
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &added); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"", "ses_test_native"} {
		if want != "" {
			stdout, stderr, code = runAgentDeck(t, home, "session", "set", added.ID, "opencode-session-id", want)
			if code != 0 {
				t.Fatalf("set failed (%d): %s\n%s", code, stdout, stderr)
			}
		}
		stdout, stderr, code = runAgentDeck(t, home, "session", "show", added.ID, "--json")
		if code != 0 {
			t.Fatalf("show failed (%d): %s\n%s", code, stdout, stderr)
		}
		var shown map[string]any
		if err := json.Unmarshal([]byte(stdout), &shown); err != nil {
			t.Fatal(err)
		}
		if got, ok := shown["opencode_session_id"]; !ok || got != want {
			t.Fatalf("OpenCode identity = %v (present %v), want %q: %s", got, ok, want, stdout)
		}
	}
}
