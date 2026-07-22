package git

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCCHook_E2E_FullLifecycle exercises the complete worktree hook lifecycle:
// configure both WorktreeCreate and WorktreeRemove hooks, create a worktree,
// verify setup.sh ran, remove the worktree, and verify the remove hook fired.
func TestCCHook_E2E_FullLifecycle(t *testing.T) {
	repoDir := t.TempDir()
	createTestRepoForSetup(t, repoDir)

	// Hook creates a real git worktree so setup.sh can run in it.
	hookWorktreePath := filepath.Join(t.TempDir(), "e2e-hook-worktree")
	createHookCmd := "git -C " + repoDir + " worktree add " + hookWorktreePath + " -b e2e-hook-branch >/dev/null 2>&1 && echo " + hookWorktreePath

	// Hook for removal: writes the payload to a marker file.
	removeMarker := filepath.Join(t.TempDir(), "remove-hook-ran")
	removeHookCmd := "bash -c 'cat > " + removeMarker + "'"

	// Write .claude/settings.json with both hooks.
	claudeDir := filepath.Join(repoDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := map[string]any{
		"hooks": map[string]any{
			"WorktreeCreate": []any{
				map[string]any{
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": createHookCmd,
						},
					},
				},
			},
			"WorktreeRemove": []any{
				map[string]any{
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": removeHookCmd,
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

	// Unused worktreePath (hook will take over).
	worktreePath := filepath.Join(t.TempDir(), "ignored")

	// Step 1: Create worktree with the hook. The hook creates a real worktree.
	var stdout, stderr bytes.Buffer
	setupErr, err := CreateWorktreeWithStateAndSetup(repoDir, worktreePath, "ignored-branch", WorktreeStateOptions{}, &stdout, &stderr, 30_000_000_000, nil)
	if err != nil {
		t.Fatalf("create worktree: %v (stderr: %s)", err, stderr.String())
	}
	if setupErr != nil {
		t.Errorf("setup error: %v (stderr: %s)", setupErr, stderr.String())
	}

	// Verify standard worktreePath was NOT created (hook took over).
	if _, statErr := os.Stat(worktreePath); statErr == nil {
		t.Error("worktreePath should not have been created by git worktree add — hook should have taken over")
	}

	// Verify hook-created worktree exists.
	if _, statErr := os.Stat(hookWorktreePath); statErr != nil {
		t.Errorf("hook-created worktree should exist: %v", statErr)
	}

	// Verify it is a proper linked worktree.
	if !IsLinkedWorktree(hookWorktreePath) {
		t.Error("hook-created worktree should be a proper linked git worktree")
	}

	// Step 2: Verify setup.sh ran.
	if _, statErr := os.Stat(sentinelPath); statErr != nil {
		t.Errorf("worktree-setup.sh should have run and created sentinel file: %v", statErr)
	}

	// Step 3: Remove the worktree. The remove hook should fire.
	if err := RemoveWorktree(repoDir, hookWorktreePath, true); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	// Step 4: Verify remove hook fired (marker file created and contains payload).
	removePayload, err := os.ReadFile(removeMarker)
	if err != nil {
		t.Fatalf("CC remove hook did not fire: %v", err)
	}
	if len(removePayload) == 0 {
		t.Fatal("CC remove hook received empty payload")
	}
	if !strings.Contains(string(removePayload), `"WorktreeRemove"`) {
		t.Fatalf("expected WorktreeRemove in payload, got: %s", removePayload)
	}

	// Step 5: Verify the worktree was actually removed.
	if _, statErr := os.Stat(hookWorktreePath); statErr == nil {
		t.Fatal("worktree should have been removed after remove hook fired")
	}
}

// TestCCHook_E2E_RegressionNoHooks verifies that without any hooks,
// the full lifecycle works as before: create → setup.sh → remove.
func TestCCHook_E2E_RegressionNoHooks(t *testing.T) {
	repoDir := t.TempDir()
	createTestRepoForSetup(t, repoDir)

	// No .claude/settings.json — no hooks configured.

	// Create .agent-deck/worktree-setup.sh that writes a sentinel file.
	agentDeckDir := filepath.Join(repoDir, ".agent-deck")
	if err := os.MkdirAll(agentDeckDir, 0o755); err != nil {
		t.Fatal(err)
	}
	worktreePath := filepath.Join(t.TempDir(), "normal-regression-worktree")
	sentinelPath := filepath.Join(worktreePath, "setup-ran")
	setupScript := "#!/bin/sh\ntouch " + sentinelPath + "\n"
	if err := os.WriteFile(filepath.Join(agentDeckDir, "worktree-setup.sh"), []byte(setupScript), 0o644); err != nil {
		t.Fatal(err)
	}

	// Step 1: Create worktree (standard git worktree add).
	var stdout, stderr bytes.Buffer
	setupErr, err := CreateWorktreeWithStateAndSetup(repoDir, worktreePath, "normal-branch", WorktreeStateOptions{}, &stdout, &stderr, 30_000_000_000, nil)
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	if setupErr != nil {
		t.Errorf("setup error: %v", setupErr)
	}

	// Verify worktree was created by git worktree add (standard path).
	if _, statErr := os.Stat(filepath.Join(worktreePath, "README.md")); statErr != nil {
		t.Errorf("worktree should have been created with README.md: %v", statErr)
	}

	// Verify it is a proper linked worktree.
	if !IsLinkedWorktree(worktreePath) {
		t.Error("expected a proper linked git worktree")
	}

	// Step 2: Verify setup.sh ran.
	if _, statErr := os.Stat(sentinelPath); statErr != nil {
		t.Errorf("worktree-setup.sh should have run and created sentinel file: %v", statErr)
	}

	// Step 3: Remove the worktree (standard git worktree remove).
	if err := RemoveWorktree(repoDir, worktreePath, true); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	// Step 4: Verify the worktree was actually removed.
	if _, statErr := os.Stat(worktreePath); statErr == nil {
		t.Fatal("worktree should have been removed")
	}
}
