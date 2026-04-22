package git

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFindWorktreeSetupScript_NotPresent(t *testing.T) {
	dir := t.TempDir()

	result := FindWorktreeSetupScript(dir)
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestFindWorktreeSetupScript_Present(t *testing.T) {
	dir := t.TempDir()

	// Create .agent-deck/worktree-setup.sh
	scriptDir := filepath.Join(dir, ".agent-deck")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(scriptDir, "worktree-setup.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := FindWorktreeSetupScript(dir)
	if result != scriptPath {
		t.Errorf("expected %q, got %q", scriptPath, result)
	}
}

func TestRunWorktreeSetupScript_Success(t *testing.T) {
	repoDir := t.TempDir()
	worktreeDir := t.TempDir()

	// Create a file in repoDir that the script will copy
	testFile := filepath.Join(repoDir, ".mcp.json")
	if err := os.WriteFile(testFile, []byte(`{"test": true}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Script copies .mcp.json using env vars
	script := `#!/bin/sh
cp "$AGENT_DECK_REPO_ROOT/.mcp.json" "$AGENT_DECK_WORKTREE_PATH/.mcp.json"
echo "copying done"
`
	scriptPath := filepath.Join(t.TempDir(), "setup.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := RunWorktreeSetupScript(scriptPath, repoDir, worktreeDir, &stdout, &stderr, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v (stderr: %s)", err, stderr.String())
	}

	// Verify file was copied
	copied, err := os.ReadFile(filepath.Join(worktreeDir, ".mcp.json"))
	if err != nil {
		t.Fatalf("expected .mcp.json to be copied: %v", err)
	}
	if string(copied) != `{"test": true}` {
		t.Errorf("unexpected content: %s", copied)
	}

	// Verify output was streamed to stdout
	if !strings.Contains(stdout.String(), "copying done") {
		t.Errorf("expected stdout to contain 'copying done', got %q", stdout.String())
	}
}

func TestRunWorktreeSetupScript_Failure(t *testing.T) {
	worktreeDir := t.TempDir()

	script := `#!/bin/sh
echo "something went wrong" >&2
exit 1
`
	scriptPath := filepath.Join(t.TempDir(), "setup.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := RunWorktreeSetupScript(scriptPath, t.TempDir(), worktreeDir, &stdout, &stderr, 0)
	if err == nil {
		t.Fatal("expected error from failing script")
	}
	if !strings.Contains(stderr.String(), "something went wrong") {
		t.Errorf("expected stderr to contain error message, got: %q", stderr.String())
	}
}

func TestRunWorktreeSetupScript_Timeout(t *testing.T) {
	worktreeDir := t.TempDir()

	script := `#!/bin/sh
sleep 300
`
	scriptPath := filepath.Join(t.TempDir(), "setup.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := RunWorktreeSetupScript(scriptPath, t.TempDir(), worktreeDir, &stdout, &stderr, 1*time.Second)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected timeout error, got: %v", err)
	}
}

// createTestRepoForSetup creates a git repo with an initial commit.
// Uses the same pattern as createTestRepo in git_test.go but avoids
// name collision since both are in the same package.
func createTestRepoForSetup(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test User"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatalf("git %s failed: %v", args[0], err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "commit", "-m", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateWorktreeWithSetup_NoScript(t *testing.T) {
	dir := t.TempDir()
	createTestRepoForSetup(t, dir)
	worktreePath := filepath.Join(dir, ".worktrees", "test-branch")

	var stdout, stderr bytes.Buffer
	setupErr, err := CreateWorktreeWithSetup(dir, worktreePath, "test-branch", &stdout, &stderr, 0)
	if err != nil {
		t.Fatalf("worktree creation failed: %v", err)
	}
	if setupErr != nil {
		t.Errorf("unexpected setup error: %v", setupErr)
	}
	if stdout.Len() != 0 {
		t.Errorf("expected no output, got %q", stdout.String())
	}

	// Verify worktree was created
	if _, err := os.Stat(filepath.Join(worktreePath, "README.md")); err != nil {
		t.Error("worktree directory should contain README.md")
	}
}

func TestCreateWorktreeWithSetup_WithScript(t *testing.T) {
	dir := t.TempDir()
	createTestRepoForSetup(t, dir)

	// Create a config file to copy
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{"ok":true}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create setup script
	scriptDir := filepath.Join(dir, ".agent-deck")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
cp "$AGENT_DECK_REPO_ROOT/.mcp.json" "$AGENT_DECK_WORKTREE_PATH/.mcp.json"
echo "setup done"
`
	if err := os.WriteFile(filepath.Join(scriptDir, "worktree-setup.sh"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}

	worktreePath := filepath.Join(dir, ".worktrees", "setup-branch")
	var stdout, stderr bytes.Buffer
	setupErr, err := CreateWorktreeWithSetup(dir, worktreePath, "setup-branch", &stdout, &stderr, 0)
	if err != nil {
		t.Fatalf("worktree creation failed: %v", err)
	}
	if setupErr != nil {
		t.Errorf("unexpected setup error: %v", setupErr)
	}
	if !strings.Contains(stdout.String(), "setup done") {
		t.Errorf("expected stdout to contain 'setup done', got %q", stdout.String())
	}

	// Verify file was copied
	data, err := os.ReadFile(filepath.Join(worktreePath, ".mcp.json"))
	if err != nil {
		t.Fatalf("expected .mcp.json to be copied: %v", err)
	}
	if string(data) != `{"ok":true}` {
		t.Errorf("unexpected content: %s", data)
	}
}

func TestCreateWorktreeWithSetup_SetupFails(t *testing.T) {
	dir := t.TempDir()
	createTestRepoForSetup(t, dir)

	// Create a failing setup script
	scriptDir := filepath.Join(dir, ".agent-deck")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
echo "fail" >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(scriptDir, "worktree-setup.sh"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}

	worktreePath := filepath.Join(dir, ".worktrees", "fail-branch")
	var stdout, stderr bytes.Buffer
	setupErr, err := CreateWorktreeWithSetup(dir, worktreePath, "fail-branch", &stdout, &stderr, 0)

	// Worktree creation should succeed
	if err != nil {
		t.Fatalf("worktree creation should succeed: %v", err)
	}

	// Setup should fail (non-fatal)
	if setupErr == nil {
		t.Error("expected setup error from failing script")
	}
	if !strings.Contains(stderr.String(), "fail") {
		t.Errorf("expected stderr to contain 'fail', got %q", stderr.String())
	}

	// Worktree should still be valid
	if _, err := os.Stat(filepath.Join(worktreePath, "README.md")); err != nil {
		t.Error("worktree should still exist after setup failure")
	}
}
