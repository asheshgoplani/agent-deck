package main

import (
	"os"
	"strings"
	"testing"
)

// Related to #1706: `session move` stored its positional path argument verbatim,
// so a relative argument became a relative project_path — resolved against the
// tmux server's cwd when the session restarts, and against a different
// directory again for the Claude history slug this command migrates. `add` has
// always resolved the same argument; move now does too.
//
// The relative argument used here is "." (the CLI subprocess inherits this test
// binary's cwd), so the test creates no directories anywhere.
func TestIssue1706_SessionMoveResolvesRelativePathArg(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	oldPath := home + "/old-proj"

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	id := sessionMoveAddSession(t, home, oldPath, "move-relative")

	stdout, stderr, code := runAgentDeck(t, home,
		"session", "move", id, ".",
		"--no-restart",
		"--json",
	)
	if code != 0 {
		t.Fatalf("session move . failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	listJSON := readSessionsJSON(t, home)
	if !strings.Contains(listJSON, cwd) {
		t.Errorf("project path was not resolved to the caller's cwd %q; list:\n%s", cwd, listJSON)
	}
	if strings.Contains(listJSON, `"path":"."`) || strings.Contains(listJSON, `"path": "."`) {
		t.Errorf("project path was stored as the literal relative argument; list:\n%s", listJSON)
	}
}
