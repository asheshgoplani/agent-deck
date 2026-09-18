package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestSessionMove_MigratesClaudeProjectDir_AccountConfigDir is the regression
// test for #2086: `session move` reported success after migrating zero
// conversation files whenever the session's account resolved to a non-default
// Claude config dir, because the migration hardcoded ~/.claude instead of the
// session's effective config dir. History is seeded under the account's
// config_dir and must land at the new slug under that same dir.
func TestSessionMove_MigratesClaudeProjectDir_AccountConfigDir(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	accountDir := filepath.Join(home, ".claude-fortiumpartners")
	adDir := filepath.Join(home, ".agent-deck")
	for _, dir := range []string{accountDir, adDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := fmt.Sprintf("[profiles.fortiumpartners.claude]\nconfig_dir = %q\n", accountDir)
	if err := os.WriteFile(filepath.Join(adDir, "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	oldPath := filepath.Join(home, "src", "gog-secure")
	newPath := filepath.Join(home, "src", "gogcli")
	if err := os.MkdirAll(newPath, 0o755); err != nil {
		t.Fatal(err)
	}
	id := sessionMoveAddSession(t, home, oldPath, "gog-secure", "--account", "fortiumpartners")

	// Seed history under the ACCOUNT config dir, not HOME/.claude.
	oldSlug := claudeProjectSlugForTest(oldPath)
	newSlug := claudeProjectSlugForTest(newPath)
	oldClaudeDir := filepath.Join(accountDir, "projects", oldSlug)
	newClaudeDir := filepath.Join(accountDir, "projects", newSlug)
	if err := os.MkdirAll(oldClaudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"turn-1.jsonl", "turn-2.jsonl"} {
		if err := os.WriteFile(filepath.Join(oldClaudeDir, name), []byte("history\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Sanity: the DEFAULT root must have nothing, matching the issue's repro.
	defaultOldDir := filepath.Join(home, ".claude", "projects", oldSlug)
	if _, err := os.Stat(defaultOldDir); !os.IsNotExist(err) {
		t.Fatalf("test setup bug: history unexpectedly exists under the default root at %s", defaultOldDir)
	}

	stdout, stderr, code := runAgentDeck(t, home,
		"session", "move", id, newPath,
		"--no-restart",
		"--json",
	)
	if code != 0 {
		t.Fatalf("session move failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	if _, err := os.Stat(oldClaudeDir); !os.IsNotExist(err) {
		t.Errorf("old account claude dir still exists at %s (should be migrated)", oldClaudeDir)
	}
	entries, err := os.ReadDir(newClaudeDir)
	if err != nil {
		t.Fatalf("new account claude dir missing at %s: %v", newClaudeDir, err)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 migrated history files at %s, got %d", newClaudeDir, len(entries))
	}

	var resp struct {
		Success           bool `json:"success"`
		HistoryFilesMoved int  `json:"history_files_moved"`
	}
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("parse move response: %v\nstdout: %s", err, stdout)
	}
	if !resp.Success {
		t.Errorf("expected success=true, got response: %s", stdout)
	}
	if resp.HistoryFilesMoved != 2 {
		t.Errorf("expected history_files_moved=2 (never success with 0 when history existed), got %d; response: %s", resp.HistoryFilesMoved, stdout)
	}
}
