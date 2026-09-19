package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestIssue2080_SessionShowJSONIncludesHookStatus asserts that
// `agent-deck session show --json <id>` reports the raw hook-driven status
// and its freshness (#2080 review of #2080's heartbeat guard).
//
// The conductor heartbeat's interactive-state guard (#1981) used to infer an
// open AskUserQuestion picker purely from a raw tmux pane-text capture. A
// real busy/interactive signal already exists: the same FRESH hook-driven
// status `session send --defer-if-busy` and the send verification loop
// (#1578, #2273) read as authoritative — a fresh "running"/"starting" hook
// status covers a picker mid-answer (its PreToolUse event never advances to
// Stop until the human responds) without guessing from glyphs. Exposing it
// here lets any caller (not just the Go send path) gate on it instead of
// re-deriving a busy verdict from pane text.
//
// A freshly-created session with no hook file yet must still report both
// keys (absence-of-field must never be confused with "hooks are absent"),
// with an empty status and fresh=false.
func TestIssue2080_SessionShowJSONIncludesHookStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	projectDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runAgentDeck(t, home,
		"add", "-t", "hookstatus-show-test", "-c", "shell", "--no-parent", "--json", projectDir,
	)
	if code != 0 {
		t.Fatalf("add failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var addResp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &addResp); err != nil {
		t.Fatalf("parse add response: %v\nstdout: %s", err, stdout)
	}

	stdout, stderr, code = runAgentDeck(t, home, "session", "show", addResp.ID, "--json")
	if code != 0 {
		t.Fatalf("session show failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("parse show response: %v\nstdout: %s", err, stdout)
	}

	statusVal, ok := got["hook_status"]
	if !ok {
		t.Fatal("session show --json omits the hook_status key entirely — callers cannot distinguish " +
			"\"hooks never fired\" from \"this build predates the field\" (#2080)")
	}
	if statusVal != "" {
		t.Errorf("hook_status = %v, want empty string for a session with no hook file yet", statusVal)
	}

	freshVal, ok := got["hook_status_fresh"]
	if !ok {
		t.Fatal("session show --json omits the hook_status_fresh key entirely (#2080)")
	}
	if freshVal != false {
		t.Errorf("hook_status_fresh = %v, want false for a session with no hook file yet", freshVal)
	}
}
