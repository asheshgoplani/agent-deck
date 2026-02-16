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

		path := GenerateWorktreePath(repoDir, branchName, "sibling")

		expected := "/path/to/my-project-feature-branch"
		if path != expected {
			t.Errorf("expected %s, got %s", expected, path)
		}
	})

	t.Run("sanitizes branch name with slashes", func(t *testing.T) {
		repoDir := "/path/to/my-project"
		branchName := "feature/new-thing"

		path := GenerateWorktreePath(repoDir, branchName, "sibling")

		expected := "/path/to/my-project-feature-new-thing"
		if path != expected {
			t.Errorf("expected %s, got %s", expected, path)
		}
	})

	t.Run("sanitizes branch name with spaces", func(t *testing.T) {
		repoDir := "/path/to/my-project"
		branchName := "feature with spaces"

		path := GenerateWorktreePath(repoDir, branchName, "sibling")

		expected := "/path/to/my-project-feature-with-spaces"
		if path != expected {
			t.Errorf("expected %s, got %s", expected, path)
		}
	})

	t.Run("subdirectory places worktree under .worktrees", func(t *testing.T) {
		repoDir := "/path/to/my-project"
		branchName := "feature-branch"

		path := GenerateWorktreePath(repoDir, branchName, "subdirectory")

		expected := "/path/to/my-project/.worktrees/feature-branch"
		if path != expected {
			t.Errorf("expected %s, got %s", expected, path)
		}
	})

	t.Run("subdirectory sanitizes slashes in branch name", func(t *testing.T) {
		repoDir := "/path/to/my-project"
		branchName := "feature/new-thing"

		path := GenerateWorktreePath(repoDir, branchName, "subdirectory")

		expected := "/path/to/my-project/.worktrees/feature-new-thing"
		if path != expected {
			t.Errorf("expected %s, got %s", expected, path)
		}
	})

	t.Run("empty location defaults to sibling", func(t *testing.T) {
		repoDir := "/path/to/my-project"
		branchName := "feature-branch"

		path := GenerateWorktreePath(repoDir, branchName, "")

		expected := "/path/to/my-project-feature-branch"
		if path != expected {
			t.Errorf("expected %s, got %s", expected, path)
		}
	})

	t.Run("explicit sibling matches empty default", func(t *testing.T) {
		repoDir := "/path/to/my-project"
		branchName := "feature-branch"

		pathEmpty := GenerateWorktreePath(repoDir, branchName, "")
		pathSibling := GenerateWorktreePath(repoDir, branchName, "sibling")

		if pathEmpty != pathSibling {
			t.Errorf("empty and sibling should produce same path: %s vs %s", pathEmpty, pathSibling)
		}
	})

	t.Run("custom absolute path", func(t *testing.T) {
		repoDir := "/path/to/my-project"
		branchName := "feature-branch"

		path := GenerateWorktreePath(repoDir, branchName, "/tmp/worktrees")

		expected := "/tmp/worktrees/my-project/feature-branch"
		if path != expected {
			t.Errorf("expected %s, got %s", expected, path)
		}
	})

	t.Run("custom path with tilde", func(t *testing.T) {
		repoDir := "/path/to/my-project"
		branchName := "feature-branch"

		path := GenerateWorktreePath(repoDir, branchName, "~/worktrees")

		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("could not get home dir: %v", err)
		}
		expected := filepath.Join(home, "worktrees", "my-project", "feature-branch")
		if path != expected {
			t.Errorf("expected %s, got %s", expected, path)
		}
	})

	t.Run("custom path sanitizes branch slashes", func(t *testing.T) {
		repoDir := "/path/to/my-project"
		branchName := "feature/my-branch"

		path := GenerateWorktreePath(repoDir, branchName, "/tmp/wt")

		expected := "/tmp/wt/my-project/feature-my-branch"
		if path != expected {
			t.Errorf("expected %s, got %s", expected, path)
		}
	})

	t.Run("custom path with trailing slash", func(t *testing.T) {
		repoDir := "/path/to/my-project"
		branchName := "main"

		path := GenerateWorktreePath(repoDir, branchName, "/tmp/worktrees/")

		// filepath.Join normalizes trailing slashes
		expected := "/tmp/worktrees/my-project/main"
		if path != expected {
			t.Errorf("expected %s, got %s", expected, path)
		}
	})

	t.Run("sibling keyword not treated as custom path", func(t *testing.T) {
		repoDir := "/path/to/my-project"
		branchName := "feature-branch"

		path := GenerateWorktreePath(repoDir, branchName, "sibling")

		// Should be sibling mode, not custom path
		expected := "/path/to/my-project-feature-branch"
		if path != expected {
			t.Errorf("expected sibling path %s, got %s", expected, path)
		}
	})

	t.Run("absolute path that looks like keyword is custom", func(t *testing.T) {
		repoDir := "/path/to/my-project"
		branchName := "feature-branch"

		path := GenerateWorktreePath(repoDir, branchName, "/sibling")

		// Contains "/" so should be treated as custom path
		expected := "/sibling/my-project/feature-branch"
		if path != expected {
			t.Errorf("expected custom path %s, got %s", expected, path)
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
	worktreePath := GenerateWorktreePath(root, "feature-test", "sibling")
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
		path := GenerateWorktreePath("/repo", "user/feature/sub", "sibling")
		if !strings.Contains(path, "user-feature-sub") {
			t.Errorf("expected sanitized path, got %s", path)
		}
	})

	t.Run("handles mixed separators", func(t *testing.T) {
		path := GenerateWorktreePath("/repo", "feature/name with spaces", "sibling")
		if strings.Contains(path, "/") && strings.Contains(path, " ") {
			t.Errorf("path should not contain slashes or spaces in branch part: %s", path)
		}
	})
}

func TestHasUncommittedChanges(t *testing.T) {
	t.Run("clean repo returns false", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)

		dirty, err := HasUncommittedChanges(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if dirty {
			t.Error("expected clean repo to have no uncommitted changes")
		}
	})

	t.Run("modified file returns true", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)

		// Modify an existing file
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("modified"), 0644); err != nil {
			t.Fatalf("failed to write file: %v", err)
		}

		dirty, err := HasUncommittedChanges(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !dirty {
			t.Error("expected modified repo to have uncommitted changes")
		}
	})

	t.Run("untracked file returns true", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)

		if err := os.WriteFile(filepath.Join(dir, "newfile.txt"), []byte("new"), 0644); err != nil {
			t.Fatalf("failed to write file: %v", err)
		}

		dirty, err := HasUncommittedChanges(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !dirty {
			t.Error("expected repo with untracked file to have uncommitted changes")
		}
	})
}

func TestGetDefaultBranch(t *testing.T) {
	t.Run("detects main branch", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)

		branch, err := GetDefaultBranch(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// createTestRepo creates a repo with default branch (main or master)
		if branch != "main" && branch != "master" {
			t.Errorf("expected main or master, got %s", branch)
		}
	})

	t.Run("returns error when no default branch exists", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)

		// Rename the default branch to something non-standard
		currentBranch, _ := GetCurrentBranch(dir)
		cmd := exec.Command("git", "branch", "-m", currentBranch, "develop")
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to rename branch: %v", err)
		}

		_, err := GetDefaultBranch(dir)
		if err == nil {
			t.Error("expected error when no main/master branch exists")
		}
	})
}

func TestDeleteBranch(t *testing.T) {
	t.Run("deletes merged branch", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)
		createBranch(t, dir, "to-delete")

		err := DeleteBranch(dir, "to-delete", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if BranchExists(dir, "to-delete") {
			t.Error("branch should have been deleted")
		}
	})

	t.Run("force deletes unmerged branch", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)

		// Create branch with a unique commit
		cmd := exec.Command("git", "checkout", "-b", "unmerged-branch")
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to create branch: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("extra"), 0644); err != nil {
			t.Fatalf("failed to write file: %v", err)
		}
		cmd = exec.Command("git", "add", ".")
		cmd.Dir = dir
		_ = cmd.Run()
		cmd = exec.Command("git", "commit", "-m", "unmerged commit")
		cmd.Dir = dir
		_ = cmd.Run()

		// Switch back to default branch
		defaultBranch, _ := GetCurrentBranch(dir)
		if defaultBranch == "unmerged-branch" {
			// Need to get the original branch name
			cmd = exec.Command("git", "checkout", "-")
			cmd.Dir = dir
			if err := cmd.Run(); err != nil {
				t.Fatalf("failed to checkout previous branch: %v", err)
			}
		}

		// Regular delete should fail
		err := DeleteBranch(dir, "unmerged-branch", false)
		if err == nil {
			t.Error("expected error deleting unmerged branch without force")
		}

		// Force delete should succeed
		err = DeleteBranch(dir, "unmerged-branch", true)
		if err != nil {
			t.Fatalf("unexpected error with force delete: %v", err)
		}

		if BranchExists(dir, "unmerged-branch") {
			t.Error("branch should have been force-deleted")
		}
	})
}

func TestMergeBranch(t *testing.T) {
	t.Run("fast-forward merge succeeds", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)

		// Create feature branch with a commit
		cmd := exec.Command("git", "checkout", "-b", "feature-merge")
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to create branch: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature"), 0644); err != nil {
			t.Fatalf("failed to write file: %v", err)
		}
		cmd = exec.Command("git", "add", ".")
		cmd.Dir = dir
		_ = cmd.Run()
		cmd = exec.Command("git", "commit", "-m", "feature commit")
		cmd.Dir = dir
		_ = cmd.Run()

		// Switch back to default branch
		cmd = exec.Command("git", "checkout", "-")
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to checkout: %v", err)
		}

		// Merge feature branch
		err := MergeBranch(dir, "feature-merge")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify the file exists after merge
		if _, err := os.Stat(filepath.Join(dir, "feature.txt")); os.IsNotExist(err) {
			t.Error("merged file should exist")
		}
	})
}

func TestPruneWorktrees(t *testing.T) {
	t.Run("prune after manually removing worktree dir", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)

		worktreePath := filepath.Join(t.TempDir(), "worktree")
		if err := CreateWorktree(dir, worktreePath, "prune-test"); err != nil {
			t.Fatalf("failed to create worktree: %v", err)
		}

		// Manually remove the worktree directory (simulates it being deleted externally)
		os.RemoveAll(worktreePath)

		// Prune should clean up the stale reference
		err := PruneWorktrees(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// After pruning, listing worktrees should show only the main one
		worktrees, err := ListWorktrees(dir)
		if err != nil {
			t.Fatalf("failed to list worktrees: %v", err)
		}
		if len(worktrees) != 1 {
			t.Errorf("expected 1 worktree after prune, got %d", len(worktrees))
		}
	})
}

func TestIsWorktree(t *testing.T) {
	t.Run("returns false for main repo", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)

		if IsWorktree(dir) {
			t.Error("expected IsWorktree to return false for main repo")
		}
	})

	t.Run("returns true for worktree", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)

		worktreePath := filepath.Join(t.TempDir(), "wt")
		if err := CreateWorktree(dir, worktreePath, "feature-wt"); err != nil {
			t.Fatalf("failed to create worktree: %v", err)
		}

		if !IsWorktree(worktreePath) {
			t.Error("expected IsWorktree to return true for worktree")
		}
	})

	t.Run("returns false for non-git directory", func(t *testing.T) {
		dir := t.TempDir()
		if IsWorktree(dir) {
			t.Error("expected IsWorktree to return false for non-git directory")
		}
	})
}

func TestGetMainWorktreePath(t *testing.T) {
	t.Run("returns main repo from worktree", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)

		worktreePath := filepath.Join(t.TempDir(), "wt")
		if err := CreateWorktree(dir, worktreePath, "feature-main"); err != nil {
			t.Fatalf("failed to create worktree: %v", err)
		}

		mainPath, err := GetMainWorktreePath(worktreePath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expectedMain, _ := filepath.EvalSymlinks(dir)
		actualMain, _ := filepath.EvalSymlinks(mainPath)

		if actualMain != expectedMain {
			t.Errorf("expected main worktree path %s, got %s", expectedMain, actualMain)
		}
	})

	t.Run("returns repo root from main repo", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)

		mainPath, err := GetMainWorktreePath(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expectedRoot, _ := filepath.EvalSymlinks(dir)
		actualRoot, _ := filepath.EvalSymlinks(mainPath)

		if actualRoot != expectedRoot {
			t.Errorf("expected %s, got %s", expectedRoot, actualRoot)
		}
	})
}

func TestGetWorktreeBaseRoot(t *testing.T) {
	t.Run("returns repo root for main repo", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)

		root, err := GetWorktreeBaseRoot(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expectedRoot, _ := filepath.EvalSymlinks(dir)
		actualRoot, _ := filepath.EvalSymlinks(root)

		if actualRoot != expectedRoot {
			t.Errorf("expected %s, got %s", expectedRoot, actualRoot)
		}
	})

	t.Run("returns main repo root from worktree", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)

		worktreePath := filepath.Join(t.TempDir(), "wt")
		if err := CreateWorktree(dir, worktreePath, "feature-base"); err != nil {
			t.Fatalf("failed to create worktree: %v", err)
		}

		root, err := GetWorktreeBaseRoot(worktreePath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expectedRoot, _ := filepath.EvalSymlinks(dir)
		actualRoot, _ := filepath.EvalSymlinks(root)

		if actualRoot != expectedRoot {
			t.Errorf("expected main repo root %s, got %s", expectedRoot, actualRoot)
		}
	})

	t.Run("returns main repo root from worktree subdirectory", func(t *testing.T) {
		dir := t.TempDir()
		createTestRepo(t, dir)

		worktreePath := filepath.Join(t.TempDir(), "wt")
		if err := CreateWorktree(dir, worktreePath, "feature-sub"); err != nil {
			t.Fatalf("failed to create worktree: %v", err)
		}

		subDir := filepath.Join(worktreePath, "deep", "nested")
		if err := os.MkdirAll(subDir, 0755); err != nil {
			t.Fatalf("failed to create subdir: %v", err)
		}

		root, err := GetWorktreeBaseRoot(subDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expectedRoot, _ := filepath.EvalSymlinks(dir)
		actualRoot, _ := filepath.EvalSymlinks(root)

		if actualRoot != expectedRoot {
			t.Errorf("expected main repo root %s, got %s", expectedRoot, actualRoot)
		}
	})

	t.Run("returns error for non-git directory", func(t *testing.T) {
		dir := t.TempDir()
		_, err := GetWorktreeBaseRoot(dir)
		if err == nil {
			t.Error("expected error for non-git directory")
		}
	})
}

func TestIntegration_WorktreeNesting(t *testing.T) {
	// This test verifies the fix for the worktree-within-worktree nesting bug.
	// When creating a worktree from within another worktree, the new worktree
	// should be a sibling (relative to the main repo), not nested inside the first.
	dir := t.TempDir()
	createTestRepo(t, dir)

	// Create first worktree (simulates Session A)
	wt1Path := filepath.Join(dir, ".worktrees", "feature-a")
	if err := CreateWorktree(dir, wt1Path, "feature-a"); err != nil {
		t.Fatalf("failed to create first worktree: %v", err)
	}

	// From inside wt1, resolve the base root (this is what the fix does)
	baseRoot, err := GetWorktreeBaseRoot(wt1Path)
	if err != nil {
		t.Fatalf("failed to get base root from worktree: %v", err)
	}

	expectedRoot, _ := filepath.EvalSymlinks(dir)
	actualRoot, _ := filepath.EvalSymlinks(baseRoot)

	if actualRoot != expectedRoot {
		t.Fatalf("GetWorktreeBaseRoot returned %s, expected %s", actualRoot, expectedRoot)
	}

	// Create second worktree using the resolved base root (simulates Session B fork)
	wt2Path := GenerateWorktreePath(baseRoot, "feature-b", "subdirectory")
	if err := CreateWorktree(baseRoot, wt2Path, "feature-b"); err != nil {
		t.Fatalf("failed to create second worktree: %v", err)
	}

	// Verify: wt2 should be under <main-repo>/.worktrees/, NOT under wt1
	expectedWt2, _ := filepath.EvalSymlinks(filepath.Join(dir, ".worktrees", "feature-b"))
	actualWt2, _ := filepath.EvalSymlinks(wt2Path)

	if actualWt2 != expectedWt2 {
		t.Errorf("second worktree nested incorrectly!\nexpected: %s\ngot:      %s", expectedWt2, actualWt2)
	}

	// Also verify that GetRepoRoot (the OLD behavior) would have caused nesting
	wrongRoot, err := GetRepoRoot(wt1Path)
	if err != nil {
		t.Fatalf("GetRepoRoot failed: %v", err)
	}
	wrongRoot, _ = filepath.EvalSymlinks(wrongRoot)
	resolvedWt1, _ := filepath.EvalSymlinks(wt1Path)
	if wrongRoot != resolvedWt1 {
		t.Logf("Note: GetRepoRoot returned %s (expected worktree root %s)", wrongRoot, resolvedWt1)
	}

	// The wrong path would be: wt1/.worktrees/feature-b (nested!)
	wrongWt2 := GenerateWorktreePath(wrongRoot, "feature-b", "subdirectory")
	if wrongWt2 == actualWt2 {
		t.Error("GetRepoRoot should have produced a DIFFERENT (nested) path than GetWorktreeBaseRoot")
	}
	t.Logf("Correct path:  %s", actualWt2)
	t.Logf("Wrong path:    %s (would have been nested)", wrongWt2)
}
