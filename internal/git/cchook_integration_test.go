package git

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeClaudeSettings writes a .claude/settings.json in repoDir with a
// WorktreeCreate hook command.
func writeClaudeSettings(t *testing.T, repoDir, command string) {
	t.Helper()
	claudeDir := filepath.Join(repoDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	settings := map[string]any{
		"hooks": map[string]any{
			"WorktreeCreate": []any{
				map[string]any{
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": command,
						},
					},
				},
			},
		},
	}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0o644); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}
}

// TestCCHook_CreateWorktree_HookTakesOver verifies that when a WorktreeCreate
// hook is configured, the hook fires and the returned path is used; the
// standard git worktree add path is skipped (worktreePath never created).
func TestCCHook_CreateWorktree_HookTakesOver(t *testing.T) {
	repoDir := t.TempDir()
	createTestRepoForSetup(t, repoDir)

	// The hook creates a plain directory (not a git worktree) and prints its path.
	hookOutputDir := filepath.Join(t.TempDir(), "hook-created")
	if err := os.MkdirAll(hookOutputDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write the hook command: just echo the pre-created directory path.
	writeClaudeSettings(t, repoDir, "echo "+hookOutputDir)

	// worktreePath is the path that would normally be used by git worktree add.
	worktreePath := filepath.Join(t.TempDir(), "should-not-be-created")

	var stdout, stderr bytes.Buffer
	effectivePath, setupErr, err := CreateWorktreeWithStateAndSetup(repoDir, worktreePath, "test-hook-branch", WorktreeStateOptions{}, &stdout, &stderr, 0, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v (stderr: %s)", err, stderr.String())
	}
	if setupErr != nil {
		t.Errorf("unexpected setup error: %v", setupErr)
	}

	// The effective path must be the hook-returned directory, not the original worktreePath.
	if effectivePath != hookOutputDir {
		t.Errorf("effectivePath = %q, want %q (hook output path)", effectivePath, hookOutputDir)
	}

	// The standard worktreePath must NOT have been created (hook took over).
	if _, statErr := os.Stat(worktreePath); statErr == nil {
		t.Error("worktreePath should not have been created by git worktree add — hook should have taken over")
	}

	// hookOutputDir still exists (hook created it before agent-deck used it).
	if _, statErr := os.Stat(hookOutputDir); statErr != nil {
		t.Errorf("hook-created directory should exist: %v", statErr)
	}
}

// TestCCHook_CreateWorktree_WithState_ErrorsOnNonGit verifies that with-state
// mode returns an error when the hook creates a plain (non-git) directory.
func TestCCHook_CreateWorktree_WithState_ErrorsOnNonGit(t *testing.T) {
	repoDir := t.TempDir()
	createTestRepoForSetup(t, repoDir)

	hookOutputDir := filepath.Join(t.TempDir(), "plain-dir")
	if err := os.MkdirAll(hookOutputDir, 0o755); err != nil {
		t.Fatal(err)
	}

	writeClaudeSettings(t, repoDir, "echo "+hookOutputDir)

	worktreePath := filepath.Join(t.TempDir(), "ignored")

	var stdout, stderr bytes.Buffer
	_, _, err := CreateWorktreeWithStateAndSetup(repoDir, worktreePath, "state-hook-branch", WorktreeStateOptions{WithState: true}, &stdout, &stderr, 0, nil)
	if err == nil {
		t.Fatal("expected error when with-state is used with a non-git hook directory")
	}
	if !strings.Contains(err.Error(), "non-git directory") {
		t.Errorf("expected error message to mention non-git directory, got: %v", err)
	}
}

// TestCCHook_CreateWorktree_SetupShStillRuns verifies that
// .agent-deck/worktree-setup.sh still runs after the CC hook returns.
func TestCCHook_CreateWorktree_SetupShStillRuns(t *testing.T) {
	repoDir := t.TempDir()
	createTestRepoForSetup(t, repoDir)

	// Hook creates a real git worktree so setup.sh can run in it.
	hookWorktreePath := filepath.Join(t.TempDir(), "hook-worktree")
	hookCmd := "git -C " + repoDir + " worktree add " + hookWorktreePath + " -b hook-setup-branch >/dev/null 2>&1 && echo " + hookWorktreePath

	writeClaudeSettings(t, repoDir, hookCmd)

	// Create .agent-deck/worktree-setup.sh that writes a sentinel file.
	agentDeckDir := filepath.Join(repoDir, ".agent-deck")
	if err := os.MkdirAll(agentDeckDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinelPath := filepath.Join(hookWorktreePath, "setup-ran")
	setupScript := "#!/bin/sh\ntouch " + sentinelPath + "\n"
	if err := os.WriteFile(filepath.Join(agentDeckDir, "worktree-setup.sh"), []byte(setupScript), 0o644); err != nil {
		t.Fatal(err)
	}

	worktreePath := filepath.Join(t.TempDir(), "ignored")

	var stdout, stderr bytes.Buffer
	_, setupErr, err := CreateWorktreeWithStateAndSetup(repoDir, worktreePath, "ignored-branch", WorktreeStateOptions{}, &stdout, &stderr, 30_000_000_000, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v (stderr: %s)", err, stderr.String())
	}
	if setupErr != nil {
		t.Errorf("unexpected setup error: %v (stderr: %s)", setupErr, stderr.String())
	}

	if _, statErr := os.Stat(sentinelPath); statErr != nil {
		t.Errorf("worktree-setup.sh should have run and created sentinel file: %v", statErr)
	}
}

// TestCCHook_CreateWorktree_NoCCHook_Unchanged verifies that when no
// .claude/settings.json exists, behavior is completely unchanged (git worktree
// add happens as usual).
func TestCCHook_CreateWorktree_NoCCHook_Unchanged(t *testing.T) {
	repoDir := t.TempDir()
	createTestRepoForSetup(t, repoDir)

	// No .claude/settings.json written — no hooks configured.
	worktreePath := filepath.Join(t.TempDir(), "normal-worktree")

	var stdout, stderr bytes.Buffer
	_, setupErr, err := CreateWorktreeWithStateAndSetup(repoDir, worktreePath, "normal-branch", WorktreeStateOptions{}, &stdout, &stderr, 0, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if setupErr != nil {
		t.Errorf("unexpected setup error: %v", setupErr)
	}

	// Standard git worktree add should have created the worktree.
	if _, statErr := os.Stat(filepath.Join(worktreePath, "README.md")); statErr != nil {
		t.Errorf("worktree should have been created with README.md: %v", statErr)
	}

	// Verify it is a real linked worktree.
	if !IsLinkedWorktree(worktreePath) {
		t.Error("expected a proper linked git worktree")
	}
}
