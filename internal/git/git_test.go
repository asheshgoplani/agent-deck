package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Helper function to create a git repo for testing
func createTestRepo(t *testing.T, dir string) {
	t.Helper()

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Configure git user for commits
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to configure git email: %v", err)
	}

	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to configure git name: %v", err)
	}

	// Create initial commit
	testFile := filepath.Join(dir, "README.md")
	if err := os.WriteFile(testFile, []byte("# Test Repo"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	cmd = exec.Command("git", "add", ".")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to git add: %v", err)
	}

	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to git commit: %v", err)
	}
}

// Helper to create a branch in a repo
func createBranch(t *testing.T, dir, branchName string) {
	t.Helper()
	cmd := exec.Command("git", "branch", branchName)
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to create branch %s: %v", branchName, err)
	}
}

func TestIsGitRepo(t *testing.T) {
	t.Run("returns true for git repo", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)

		if !IsGitRepo(dir) {
			t.Error("expected IsGitRepo to return true for a git repo")
		}
	})

	t.Run("returns true for subdirectory of git repo", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)

		subDir := filepath.Join(dir, "subdir")
		if err := os.MkdirAll(subDir, 0755); err != nil {
			t.Fatalf("failed to create subdir: %v", err)
		}

		if !IsGitRepo(subDir) {
			t.Error("expected IsGitRepo to return true for subdirectory of git repo")
		}
	})

	t.Run("returns false for non-git directory", func(t *testing.T) {
		dir := t.TempDir()

		if IsGitRepo(dir) {
			t.Error("expected IsGitRepo to return false for non-git directory")
		}
	})

	t.Run("returns false for non-existent directory", func(t *testing.T) {
		if IsGitRepo("/nonexistent/path/that/does/not/exist") {
			t.Error("expected IsGitRepo to return false for non-existent directory")
		}
	})
}

func TestGetRepoRoot(t *testing.T) {
	t.Run("returns repo root for git repo", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)

		root, err := GetRepoRoot(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Resolve symlinks for comparison (macOS /tmp is a symlink)
		expectedRoot, _ := filepath.EvalSymlinks(dir)
		actualRoot, _ := filepath.EvalSymlinks(root)

		if actualRoot != expectedRoot {
			t.Errorf("expected root %s, got %s", expectedRoot, actualRoot)
		}
	})

	t.Run("returns repo root from subdirectory", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)

		subDir := filepath.Join(dir, "subdir", "nested")
		if err := os.MkdirAll(subDir, 0755); err != nil {
			t.Fatalf("failed to create subdir: %v", err)
		}

		root, err := GetRepoRoot(subDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expectedRoot, _ := filepath.EvalSymlinks(dir)
		actualRoot, _ := filepath.EvalSymlinks(root)

		if actualRoot != expectedRoot {
			t.Errorf("expected root %s, got %s", expectedRoot, actualRoot)
		}
	})

	t.Run("returns error for non-git directory", func(t *testing.T) {
		dir := t.TempDir()

		_, err := GetRepoRoot(dir)
		if err == nil {
			t.Error("expected error for non-git directory")
		}
	})
}

func TestGetCurrentBranch(t *testing.T) {
	t.Run("returns main/master for new repo", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)

		branch, err := GetCurrentBranch(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Could be main or master depending on git config
		if branch != "main" && branch != "master" {
			t.Errorf("expected main or master, got %s", branch)
		}
	})

	t.Run("returns correct branch after checkout", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)
		createBranch(t, dir, "feature-branch")

		cmd := exec.Command("git", "checkout", "feature-branch")
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to checkout branch: %v", err)
		}

		branch, err := GetCurrentBranch(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if branch != "feature-branch" {
			t.Errorf("expected feature-branch, got %s", branch)
		}
	})

	t.Run("returns error for non-git directory", func(t *testing.T) {
		dir := t.TempDir()

		_, err := GetCurrentBranch(dir)
		if err == nil {
			t.Error("expected error for non-git directory")
		}
	})
}

func TestBranchExists(t *testing.T) {
	t.Run("returns true for existing branch", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)
		createBranch(t, dir, "existing-branch")

		if !BranchExists(dir, "existing-branch") {
			t.Error("expected BranchExists to return true for existing branch")
		}
	})

	t.Run("returns false for non-existing branch", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)

		if BranchExists(dir, "nonexistent-branch") {
			t.Error("expected BranchExists to return false for non-existing branch")
		}
	})

	t.Run("returns false for non-git directory", func(t *testing.T) {
		dir := t.TempDir()

		if BranchExists(dir, "any-branch") {
			t.Error("expected BranchExists to return false for non-git directory")
		}
	})
}

func TestValidateBranchName(t *testing.T) {
	t.Run("accepts valid branch names", func(t *testing.T) {
		validNames := []string{
			"feature-branch",
			"feature/new-thing",
			"bugfix-123",
			"release-v1.0.0",
			"user/feature",
		}

		for _, name := range validNames {
			if err := ValidateBranchName(name); err != nil {
				t.Errorf("expected %q to be valid, got error: %v", name, err)
			}
		}
	})

	t.Run("rejects invalid branch names", func(t *testing.T) {
		invalidNames := []string{
			"",               // empty
			".hidden",        // starts with dot
			"branch..double", // double dots
			"branch.lock",    // ends with .lock
			"branch ",        // trailing space
			" branch",        // leading space
			"branch\tname",   // contains tab
			"branch~name",    // contains tilde
			"branch^name",    // contains caret
			"branch:name",    // contains colon
			"branch?name",    // contains question mark
			"branch*name",    // contains asterisk
			"branch[name",    // contains open bracket
			"branch\\name",   // contains backslash
			"@",              // just @
			"branch@{name",   // contains @{
		}

		for _, name := range invalidNames {
			if err := ValidateBranchName(name); err == nil {
				t.Errorf("expected %q to be invalid, but got no error", name)
			}
		}
	})
}

func TestGenerateWorktreePath(t *testing.T) {
	t.Run("generates sibling path with branch suffix", func(t *testing.T) {
		repoDir := "/path/to/my-project"
		branchName := "feature-branch"

		path := GenerateWorktreePath(repoDir, branchName)

		expected := "/path/to/my-project-feature-branch"
		if path != expected {
			t.Errorf("expected %s, got %s", expected, path)
		}
	})

	t.Run("sanitizes branch name with slashes", func(t *testing.T) {
		repoDir := "/path/to/my-project"
		branchName := "feature/new-thing"

		path := GenerateWorktreePath(repoDir, branchName)

		expected := "/path/to/my-project-feature-new-thing"
		if path != expected {
			t.Errorf("expected %s, got %s", expected, path)
		}
	})

	t.Run("sanitizes branch name with spaces", func(t *testing.T) {
		repoDir := "/path/to/my-project"
		branchName := "feature with spaces"

		path := GenerateWorktreePath(repoDir, branchName)

		expected := "/path/to/my-project-feature-with-spaces"
		if path != expected {
			t.Errorf("expected %s, got %s", expected, path)
		}
	})
}

func TestCreateWorktree(t *testing.T) {
	t.Run("creates worktree with existing branch", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)
		createBranch(t, dir, "existing-branch")

		worktreePath := filepath.Join(t.TempDir(), "worktree")

		err := CreateWorktree(dir, worktreePath, "existing-branch")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify worktree was created
		if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
			t.Error("worktree directory was not created")
		}

		// Verify it's on the correct branch
		branch, err := GetCurrentBranch(worktreePath)
		if err != nil {
			t.Fatalf("failed to get branch: %v", err)
		}
		if branch != "existing-branch" {
			t.Errorf("expected branch existing-branch, got %s", branch)
		}
	})

	t.Run("creates worktree with new branch", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)

		worktreePath := filepath.Join(t.TempDir(), "worktree")

		err := CreateWorktree(dir, worktreePath, "new-branch")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify worktree was created
		if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
			t.Error("worktree directory was not created")
		}

		// Verify it's on the new branch
		branch, err := GetCurrentBranch(worktreePath)
		if err != nil {
			t.Fatalf("failed to get branch: %v", err)
		}
		if branch != "new-branch" {
			t.Errorf("expected branch new-branch, got %s", branch)
		}
	})

	t.Run("returns error for invalid branch name", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)

		worktreePath := filepath.Join(t.TempDir(), "worktree")

		err := CreateWorktree(dir, worktreePath, "invalid..branch")
		if err == nil {
			t.Error("expected error for invalid branch name")
		}
	})

	t.Run("returns error for non-git directory", func(t *testing.T) {
		dir := t.TempDir()
		worktreePath := filepath.Join(t.TempDir(), "worktree")

		err := CreateWorktree(dir, worktreePath, "branch")
		if err == nil {
			t.Error("expected error for non-git directory")
		}
	})
}

func TestListWorktrees(t *testing.T) {
	t.Run("lists worktrees in repo", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)

		// Create a worktree
		worktreePath := filepath.Join(t.TempDir(), "worktree")
		if err := CreateWorktree(dir, worktreePath, "feature-branch"); err != nil {
			t.Fatalf("failed to create worktree: %v", err)
		}

		worktrees, err := ListWorktrees(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should have at least 2 worktrees (main + feature)
		if len(worktrees) < 2 {
			t.Errorf("expected at least 2 worktrees, got %d", len(worktrees))
		}

		// Find the feature worktree
		var found bool
		for _, wt := range worktrees {
			resolvedPath, _ := filepath.EvalSymlinks(wt.Path)
			resolvedWorktreePath, _ := filepath.EvalSymlinks(worktreePath)
			if resolvedPath == resolvedWorktreePath {
				found = true
				if wt.Branch != "feature-branch" {
					t.Errorf("expected branch feature-branch, got %s", wt.Branch)
				}
			}
		}
		if !found {
			t.Error("feature worktree not found in list")
		}
	})

	t.Run("returns error for non-git directory", func(t *testing.T) {
		dir := t.TempDir()

		_, err := ListWorktrees(dir)
		if err == nil {
			t.Error("expected error for non-git directory")
		}
	})
}

func TestRemoveWorktree(t *testing.T) {
	t.Run("removes worktree", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)

		worktreePath := filepath.Join(t.TempDir(), "worktree")
		if err := CreateWorktree(dir, worktreePath, "feature-branch"); err != nil {
			t.Fatalf("failed to create worktree: %v", err)
		}

		err := RemoveWorktree(dir, worktreePath, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify worktree was removed from list
		worktrees, err := ListWorktrees(dir)
		if err != nil {
			t.Fatalf("failed to list worktrees: %v", err)
		}

		resolvedWorktreePath, _ := filepath.EvalSymlinks(worktreePath)
		for _, wt := range worktrees {
			resolvedPath, _ := filepath.EvalSymlinks(wt.Path)
			if resolvedPath == resolvedWorktreePath {
				t.Error("worktree was not removed from list")
			}
		}
	})

	t.Run("force removes worktree with changes", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)

		worktreePath := filepath.Join(t.TempDir(), "worktree")
		if err := CreateWorktree(dir, worktreePath, "feature-branch"); err != nil {
			t.Fatalf("failed to create worktree: %v", err)
		}

		// Make uncommitted changes
		testFile := filepath.Join(worktreePath, "newfile.txt")
		if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}

		err := RemoveWorktree(dir, worktreePath, true)
		if err != nil {
			t.Fatalf("unexpected error with force: %v", err)
		}
	})

	t.Run("returns error for non-existent worktree", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)

		err := RemoveWorktree(dir, "/nonexistent/worktree", false)
		if err == nil {
			t.Error("expected error for non-existent worktree")
		}
	})
}

func TestWorktreeStruct(t *testing.T) {
	t.Run("worktree has expected fields", func(t *testing.T) {
		wt := Worktree{
			Path:   "/path/to/worktree",
			Branch: "feature-branch",
			Commit: "abc123",
			Bare:   false,
		}

		if wt.Path != "/path/to/worktree" {
			t.Errorf("unexpected path: %s", wt.Path)
		}
		if wt.Branch != "feature-branch" {
			t.Errorf("unexpected branch: %s", wt.Branch)
		}
		if wt.Commit != "abc123" {
			t.Errorf("unexpected commit: %s", wt.Commit)
		}
		if wt.Bare != false {
			t.Error("unexpected bare value")
		}
	})
}

func TestIntegration_WorktreeLifecycle(t *testing.T) {
	// Full lifecycle test: create repo -> create worktree -> list -> remove
	dir := t.TempDir()
	createTestRepo(t, dir)

	// Verify initial state
	if !IsGitRepo(dir) {
		t.Fatal("test repo is not a git repo")
	}

	root, err := GetRepoRoot(dir)
	if err != nil {
		t.Fatalf("failed to get repo root: %v", err)
	}

	branch, err := GetCurrentBranch(dir)
	if err != nil {
		t.Fatalf("failed to get current branch: %v", err)
	}
	t.Logf("Initial branch: %s", branch)

	// Create worktree
	worktreePath := GenerateWorktreePath(root, "feature-test")
	t.Logf("Creating worktree at: %s", worktreePath)

	if err := CreateWorktree(root, worktreePath, "feature-test"); err != nil {
		t.Fatalf("failed to create worktree: %v", err)
	}

	// List and verify
	worktrees, err := ListWorktrees(root)
	if err != nil {
		t.Fatalf("failed to list worktrees: %v", err)
	}

	if len(worktrees) != 2 {
		t.Errorf("expected 2 worktrees, got %d", len(worktrees))
	}

	// Verify branch exists now
	if !BranchExists(root, "feature-test") {
		t.Error("feature-test branch should exist after worktree creation")
	}

	// Remove worktree
	if err := RemoveWorktree(root, worktreePath, false); err != nil {
		t.Fatalf("failed to remove worktree: %v", err)
	}

	// Verify removal
	worktrees, err = ListWorktrees(root)
	if err != nil {
		t.Fatalf("failed to list worktrees after removal: %v", err)
	}

	if len(worktrees) != 1 {
		t.Errorf("expected 1 worktree after removal, got %d", len(worktrees))
	}

	// Cleanup - remove the worktree directory if it still exists
	os.RemoveAll(worktreePath)
}

func TestGenerateWorktreePath_EdgeCases(t *testing.T) {
	t.Run("handles multiple slashes", func(t *testing.T) {
		path := GenerateWorktreePath("/repo", "user/feature/sub")
		if !strings.Contains(path, "user-feature-sub") {
			t.Errorf("expected sanitized path, got %s", path)
		}
	})

	t.Run("handles mixed separators", func(t *testing.T) {
		path := GenerateWorktreePath("/repo", "feature/name with spaces")
		if strings.Contains(path, "/") && strings.Contains(path, " ") {
			t.Errorf("path should not contain slashes or spaces in branch part: %s", path)
		}
	})
}
