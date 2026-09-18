package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2299 CLI evidence: `agent-deck session output <target> -q` must not
// return a sibling session's transcript when the target has no JSONL of its
// own. Uses the existing subprocess helper (runAgentDeck) against a sandbox
// HOME — no live Claude process.
func TestIssue2299_CLISessionOutputDoesNotReturnSiblingTranscript(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}

	home := t.TempDir()
	project := filepath.Join(home, "shared-project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}

	oldDeckID := sessionMoveAddSession(t, home, project, "old-sibling")
	newDeckID := sessionMoveAddSession(t, home, project, "new-target")

	const (
		oldClaude = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		newClaude = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
		oldReply  = "OLD SESSION RESPONSE"
	)
	if stdout, stderr, code := runAgentDeck(t, home, "session", "set", oldDeckID, "claude-session-id", oldClaude); code != 0 {
		t.Fatalf("set old claude-session-id failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if stdout, stderr, code := runAgentDeck(t, home, "session", "set", newDeckID, "claude-session-id", newClaude); code != 0 {
		t.Fatalf("set new claude-session-id failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	resolved := project
	if r, err := filepath.EvalSymlinks(project); err == nil {
		resolved = r
	}
	projectsDir := filepath.Join(home, ".claude", "projects", session.ConvertToClaudeDirName(resolved))
	if err := os.MkdirAll(projectsDir, 0o755); err != nil {
		t.Fatalf("mkdir projects dir: %v", err)
	}
	writeClaudeJSONL(t, projectsDir, oldClaude, "hi", oldReply, "2026-05-31T22:05:00Z")

	stdout, stderr, code := runAgentDeck(t, home, "session", "output", newDeckID, "-q")
	if code != 0 {
		t.Fatalf("session output failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if strings.Contains(stdout, oldReply) {
		t.Fatalf("session output -q returned sibling transcript %q\nstderr: %s", stdout, stderr)
	}
	if got := strings.TrimSpace(stdout); got != "" {
		t.Fatalf("session output -q = %q, want empty", got)
	}
}
