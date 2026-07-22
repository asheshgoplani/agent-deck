package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoveWorktree_CCHook_Fires(t *testing.T) {
	dir := t.TempDir()
	createTestRepoForSetup(t, dir)

	marker := filepath.Join(dir, "cc-remove-ran")
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := `{"hooks":{"WorktreeRemove":[{"hooks":[{"type":"command","command":"bash -c 'cat > ` + marker + `'"}]}]}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}

	wtPath := filepath.Join(dir, ".worktrees", "removable")
	if err := CreateWorktree(dir, wtPath, "removable"); err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	if err := RemoveWorktree(dir, wtPath, true); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("CC remove hook did not fire: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("CC remove hook received empty payload")
	}
	// Verify the payload contains correct event name
	if !strings.Contains(string(data), `"WorktreeRemove"`) {
		t.Fatalf("expected WorktreeRemove in payload, got: %s", data)
	}
}

func TestRemoveWorktree_CCHook_FailureDoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	createTestRepoForSetup(t, dir)

	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := `{"hooks":{"WorktreeRemove":[{"hooks":[{"type":"command","command":"exit 1"}]}]}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}

	wtPath := filepath.Join(dir, ".worktrees", "doomed")
	if err := CreateWorktree(dir, wtPath, "doomed"); err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	if err := RemoveWorktree(dir, wtPath, true); err != nil {
		t.Fatalf("removal should succeed even when CC hook fails: %v", err)
	}

	if _, err := os.Stat(wtPath); err == nil {
		t.Fatal("worktree should have been removed despite hook failure")
	}
}

func TestRemoveWorktree_CCHook_NoCCHook_Unchanged(t *testing.T) {
	dir := t.TempDir()
	createTestRepoForSetup(t, dir)

	// No .claude/settings.json — should work exactly as before
	wtPath := filepath.Join(dir, ".worktrees", "normal")
	if err := CreateWorktree(dir, wtPath, "normal"); err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	if err := RemoveWorktree(dir, wtPath, true); err != nil {
		t.Fatalf("remove should work without CC hooks: %v", err)
	}
}
