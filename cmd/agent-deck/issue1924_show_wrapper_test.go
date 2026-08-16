package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestIssue1924_SessionShowJSONIncludesWrapper asserts that
// `agent-deck session show --json <id>` reports the wrapper field.
//
// This is gh#615 again, one field over. Two separate JSON emitters: handleList
// threads the value, handleSessionShow builds its own jsonData map and never
// included wrapper. The data persists correctly — verified end to end through a
// real storage round trip — so the only thing missing was the report.
//
// The cost of that gap is what makes it worth a test rather than a one-line
// patch: `session show --json` is the natural way to verify `session set
// wrapper`, and with the key absent `.wrapper` reads back as null. #1924 was
// filed as "the write did not persist", which sent the investigation at the
// mutator and the storage layer, neither of which was at fault.
//
// Emitted unconditionally, matching the channels field's stated reasoning:
// omitting when empty makes absence-of-field ambiguous with absence-of-value.
func TestIssue1924_SessionShowJSONIncludesWrapper(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	projectDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runAgentDeck(t, home,
		"add", "-t", "wrap-show-test", "-c", "shell", "--no-parent", "--json", projectDir,
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

	// A session with no wrapper must still report the key, so "not set" is
	// distinguishable from "not reported".
	stdout, stderr, code = runAgentDeck(t, home, "session", "show", addResp.ID, "--json")
	if code != 0 {
		t.Fatalf("session show failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var before map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &before); err != nil {
		t.Fatalf("parse show response: %v\nstdout: %s", err, stdout)
	}
	if _, ok := before["wrapper"]; !ok {
		t.Error("session show --json omits the wrapper key entirely; absent reads as null and is indistinguishable from an unpersisted write (#1924)")
	}

	const want = "env CODEX_HOME=/tmp/acct {command}"
	stdout, stderr, code = runAgentDeck(t, home, "session", "set", addResp.ID, "wrapper", want)
	if code != 0 {
		t.Fatalf("session set wrapper failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	stdout, stderr, code = runAgentDeck(t, home, "session", "show", addResp.ID, "--json")
	if code != 0 {
		t.Fatalf("session show after set failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var after map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &after); err != nil {
		t.Fatalf("parse show response: %v\nstdout: %s", err, stdout)
	}
	got, ok := after["wrapper"]
	if !ok {
		t.Fatal("session show --json still omits wrapper after it was set (#1924)")
	}
	if got != want {
		t.Errorf("wrapper = %v, want %q — the success line said it was set", got, want)
	}
}
