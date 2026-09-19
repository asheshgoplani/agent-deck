package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestSessionMove_MigratesClaudeProjectDir_GroupConfigDirBoundary is the
// regression test for the review HOLD on #2086: `session move <id> <path>
// --group <target>` resolved the (single) migration config dir from the
// OLD group, then switched inst.GroupPath to the NEW group AFTER migrating.
// When the target group has its own [groups."<target>".claude].config_dir,
// the restarted session resolves Claude history under the new group's
// config dir while the migrated files sit under the old group's config
// dir — silent history loss, the same shape #2086 was filed for, one level
// removed (via --group instead of via account).
func TestSessionMove_MigratesClaudeProjectDir_GroupConfigDirBoundary(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	sourceDir := filepath.Join(home, ".claude-source-group")
	targetDir := filepath.Join(home, ".claude-target-group")
	adDir := filepath.Join(home, ".agent-deck")
	for _, dir := range []string{sourceDir, targetDir, adDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := fmt.Sprintf(
		"[groups.\"source-team\".claude]\nconfig_dir = %q\n\n[groups.\"target-team\".claude]\nconfig_dir = %q\n",
		sourceDir, targetDir,
	)
	if err := os.WriteFile(filepath.Join(adDir, "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	oldPath := filepath.Join(home, "src", "group-proj-old")
	newPath := filepath.Join(home, "src", "group-proj-new")
	if err := os.MkdirAll(newPath, 0o755); err != nil {
		t.Fatal(err)
	}
	id := sessionMoveAddSession(t, home, oldPath, "group-boundary", "--group", "source-team")

	// Seed history under the SOURCE group's config dir.
	oldSlug := claudeProjectSlugForTest(oldPath)
	newSlug := claudeProjectSlugForTest(newPath)
	oldClaudeDir := filepath.Join(sourceDir, "projects", oldSlug)
	newClaudeDirInTarget := filepath.Join(targetDir, "projects", newSlug)
	if err := os.MkdirAll(oldClaudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"turn-1.jsonl", "turn-2.jsonl"} {
		if err := os.WriteFile(filepath.Join(oldClaudeDir, name), []byte("history\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	stdout, stderr, code := runAgentDeck(t, home,
		"session", "move", id, newPath,
		"--group", "target-team",
		"--no-restart",
		"--json",
	)
	if code != 0 {
		t.Fatalf("session move failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	// The bug: history stays under sourceDir/projects/<oldSlug> (or moves to
	// sourceDir/projects/<newSlug>, still the wrong config dir) instead of
	// landing under targetDir/projects/<newSlug>, which is where the
	// restarted session (now in target-team) will actually look.
	if _, err := os.Stat(oldClaudeDir); !os.IsNotExist(err) {
		t.Errorf("old source-group claude dir still exists at %s (should be migrated)", oldClaudeDir)
	}
	entries, err := os.ReadDir(newClaudeDirInTarget)
	if err != nil {
		t.Fatalf("new target-group claude dir missing at %s: %v (history was left under the wrong config dir)", newClaudeDirInTarget, err)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 migrated history files at %s, got %d", newClaudeDirInTarget, len(entries))
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
		t.Errorf("expected history_files_moved=2, got %d; response: %s", resp.HistoryFilesMoved, stdout)
	}
}

// TestSessionMove_MigratesClaudeProjectDir_SameConfigDirGroupMove is the
// same-dir control case: moving --group to a group with NO config_dir
// override (or the same one) must still behave exactly like a plain path
// move — a rename within the single config dir, not a no-op or a spurious
// cross-dir copy.
func TestSessionMove_MigratesClaudeProjectDir_SameConfigDirGroupMove(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	oldPath := filepath.Join(home, "src", "same-dir-old")
	newPath := filepath.Join(home, "src", "same-dir-new")
	if err := os.MkdirAll(newPath, 0o755); err != nil {
		t.Fatal(err)
	}

	id := sessionMoveAddSession(t, home, oldPath, "same-dir-group-move")
	oldClaudeDir := seedClaudeProjectDir(t, home, oldPath, "turn-1\n")
	newClaudeDir := filepath.Join(home, ".claude", "projects", claudeProjectSlugForTest(newPath))

	stdout, stderr, code := runAgentDeck(t, home,
		"session", "move", id, newPath,
		"--group", "some-plain-group",
		"--no-restart",
		"--json",
	)
	if code != 0 {
		t.Fatalf("session move failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	if _, err := os.Stat(oldClaudeDir); !os.IsNotExist(err) {
		t.Errorf("old claude dir still exists at %s (should be migrated)", oldClaudeDir)
	}
	if _, err := os.Stat(newClaudeDir); os.IsNotExist(err) {
		t.Fatalf("new claude dir missing at %s", newClaudeDir)
	}

	var resp struct {
		HistoryFilesMoved int `json:"history_files_moved"`
	}
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("parse move response: %v\nstdout: %s", err, stdout)
	}
	if resp.HistoryFilesMoved != 1 {
		t.Errorf("expected history_files_moved=1, got %d; response: %s", resp.HistoryFilesMoved, stdout)
	}
}
