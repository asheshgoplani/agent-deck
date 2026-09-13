package main

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// `list --json` must print a JSON array even when there is nothing to list.
// Before, an empty profile printed the human sentinel "No sessions found in
// profile '<p>'." (exit 0) and `list --all --json` printed `null`, so every
// consumer that decodes stdout as an array failed on a fresh host.
func TestListJSON_EmptyProfilePrintsEmptyArray(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH; the list CLI requires tmux in PATH")
	}
	home := t.TempDir()

	for _, args := range [][]string{
		{"list", "--json"},
		{"list", "--all", "--json"},
	} {
		stdout, stderr, code := runAgentDeck(t, home, args...)
		if code != 0 {
			t.Fatalf("%v: exit %d\nstdout: %s\nstderr: %s", args, code, stdout, stderr)
		}
		var rows []map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &rows); err != nil || rows == nil || len(rows) != 0 {
			t.Fatalf("%v: want an empty JSON array, got %q (err %v)", args, stdout, err)
		}
	}

	// The human-readable output is unchanged.
	stdout, stderr, code := runAgentDeck(t, home, "list")
	if code != 0 || !strings.Contains(stdout, "No sessions found in profile") {
		t.Fatalf("plain list: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
}
