package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindWorktreeDestructionScript(t *testing.T) {
	dir := t.TempDir()
	if p, _ := FindWorktreeDestructionScript(dir); p != "" {
		t.Fatalf("expected empty, got %q", p)
	}

	scriptDir := filepath.Join(dir, ".agent-deck")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(scriptDir, "worktree-destruction.sh")
	if err := os.WriteFile(want, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := FindWorktreeDestructionScript(dir); got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

// TestRemoveWorktree_RunsDestructionScript is the end-to-end check: under
// the default "prompt" policy the hook only fires once approved, and then
// before removal, in the worktree dir, with the env vars set.
func TestRemoveWorktree_RunsDestructionScript(t *testing.T) {
	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentPrompt})
	dir := t.TempDir()
	createTestRepoForSetup(t, dir)

	scriptDir := filepath.Join(dir, ".agent-deck")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Records env vars + cwd to a marker file in the repo root so we can assert
	// the script ran with the worktree still present.
	script := `#!/bin/sh
echo "$AGENT_DECK_REPO_ROOT|$AGENT_DECK_WORKTREE_PATH|$(pwd)" > "$AGENT_DECK_REPO_ROOT/destruction-ran"
`
	if err := os.WriteFile(filepath.Join(scriptDir, "worktree-destruction.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	// Unapproved: removal proceeds, the hook does not run.
	unapproved := filepath.Join(dir, ".worktrees", "unapproved")
	if err := CreateWorktree(dir, unapproved, "unapproved"); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	if err := RemoveWorktree(dir, unapproved, true); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "destruction-ran")); !os.IsNotExist(err) {
		t.Fatalf("unapproved destruction hook ran (stat err = %v)", err)
	}
	if _, err := os.Stat(unapproved); !os.IsNotExist(err) {
		t.Errorf("expected unapproved worktree removed anyway, stat err = %v", err)
	}

	if _, err := TrustScript(dir, "destruction", filepath.Join(scriptDir, "worktree-destruction.sh")); err != nil {
		t.Fatalf("trust destruction hook: %v", err)
	}

	worktreePath := filepath.Join(dir, ".worktrees", "doomed")
	if err := CreateWorktree(dir, worktreePath, "doomed"); err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	if err := RemoveWorktree(dir, worktreePath, true); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	marker, err := os.ReadFile(filepath.Join(dir, "destruction-ran"))
	if err != nil {
		t.Fatalf("destruction script did not run: %v", err)
	}
	got := string(marker)
	// AGENT_DECK_WORKTREE_PATH must be the worktree, and cwd must have been it.
	if want := worktreePath + "|"; !strings.Contains(got, want) {
		t.Errorf("expected worktree path %q in marker, got %q", worktreePath, got)
	}

	// Worktree should be gone.
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Errorf("expected worktree removed, stat err = %v", err)
	}
}
