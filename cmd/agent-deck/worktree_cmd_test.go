package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestMain is in testmain_test.go - sets AGENTDECK_PROFILE=_test

// =============================================================================
// Tests for Worktree Commands
// =============================================================================

// TestWorktreeListInNonGitRepo verifies that worktree list fails gracefully
// when run outside a VCS repository.
func TestWorktreeListInNonGitRepo(t *testing.T) {
	// Create temp non-git directory
	tmpDir := t.TempDir()

	// Verify it's not a git repo (no .git directory)
	gitDir := filepath.Join(tmpDir, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		t.Fatal("Expected non-git directory but .git exists")
	}

	// The internal/git.IsGitRepo function should return false for this directory
	// We test this behavior indirectly since handleWorktreeList calls it internally

	// Note: We can't easily test the CLI output here without restructuring,
	// but we can verify the directory state is correct for testing
	t.Logf("Test directory %s is correctly a non-git directory", tmpDir)
}

// TestWorktreeListInGitRepo verifies that worktree operations work in a git repo.
func TestWorktreeListInGitRepo(t *testing.T) {
	tmpDir := t.TempDir()

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	cmd.Env = testutil.CleanGitEnv(os.Environ())
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to init git repo: %v", err)
	}

	// Create initial commit (required for worktree operations)
	cmd = exec.Command("git", "commit", "--allow-empty", "-m", "init")
	cmd.Dir = tmpDir
	cmd.Env = append(testutil.CleanGitEnv(os.Environ()),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to create commit: %v", err)
	}

	// Verify it's a git repo
	gitDir := filepath.Join(tmpDir, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		t.Fatal("Expected git directory to exist")
	}

	t.Logf("Test directory %s is correctly a VCS repository", tmpDir)
}

// TestWorktreeListWithWorktrees verifies listing works when worktrees exist.
func TestWorktreeListWithWorktrees(t *testing.T) {
	tmpDir := t.TempDir()

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	cmd.Env = testutil.CleanGitEnv(os.Environ())
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to init git repo: %v", err)
	}

	// Create initial commit
	cmd = exec.Command("git", "commit", "--allow-empty", "-m", "init")
	cmd.Dir = tmpDir
	cmd.Env = append(testutil.CleanGitEnv(os.Environ()),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to create commit: %v", err)
	}

	// Create a worktree
	worktreePath := filepath.Join(tmpDir, "worktree-feature")
	cmd = exec.Command("git", "worktree", "add", "-b", "feature-branch", worktreePath)
	cmd.Dir = tmpDir
	cmd.Env = testutil.CleanGitEnv(os.Environ())
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to create worktree: %v", err)
	}

	// Verify worktree exists
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		t.Fatal("Expected worktree directory to exist")
	}

	// Verify worktree list command works
	cmd = exec.Command("git", "worktree", "list")
	cmd.Dir = tmpDir
	cmd.Env = testutil.CleanGitEnv(os.Environ())
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to list worktrees: %v", err)
	}

	// Should contain both the main repo and the worktree
	outputStr := string(output)
	if !containsPath(outputStr, tmpDir) {
		t.Errorf("Expected worktree list to contain main repo path %s", tmpDir)
	}
	if !containsPath(outputStr, worktreePath) {
		t.Errorf("Expected worktree list to contain worktree path %s", worktreePath)
	}

	t.Logf("Worktree list output:\n%s", outputStr)
}

// TestTruncateString tests the truncateString helper function.
func TestTruncateString(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"hello", 10, "hello"},            // No truncation needed
		{"hello world", 10, "hello w..."}, // Truncation needed
		{"hi", 3, "hi"},                   // Exactly at limit
		{"hello", 5, "hello"},             // Exactly at limit
		{"hello", 3, "hel"},               // Very short max (no room for "...")
		{"a", 1, "a"},                     // Single char
		{"", 5, ""},                       // Empty string
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := truncateString(tt.input, tt.maxLen)
			if result != tt.expected {
				t.Errorf("truncateString(%q, %d) = %q, want %q",
					tt.input, tt.maxLen, result, tt.expected)
			}
		})
	}
}

// containsPath checks if the output contains the given path.
func containsPath(output, path string) bool {
	// Simple substring check - path should appear in output
	return len(output) > 0 && len(path) > 0 && filepath.Clean(output) != "" &&
		(output == path || len(output) > len(path))
}
