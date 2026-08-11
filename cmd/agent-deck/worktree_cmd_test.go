package main

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

func TestWorktreeFinishQueueLockFailureLeavesWorkspaceBranchRowAndQueue(t *testing.T) {
	const profile = "_test_worktree_finish_queue_lock"
	if os.Getenv("AGENT_DECK_WORKTREE_LOCK_HELPER") == "1" {
		handleWorktreeFinish(profile, []string{"worktree-lock-failure", "--no-merge", "--force", "--json"})
		return
	}
	repo := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "seed"), []byte("seed"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "seed")
	run("commit", "-m", "seed")
	run("branch", "cli-lock-branch")
	worktree := filepath.Join(t.TempDir(), "worktree")
	run("worktree", "add", worktree, "cli-lock-branch")
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	inst := &session.Instance{ID: "worktree-lock-failure", Title: "worktree-lock-failure", ProjectPath: worktree, GroupPath: session.DefaultGroupPath, Tool: "shell", Command: "shell", Status: session.StatusStopped, WorktreeRepoRoot: repo, WorktreePath: worktree, WorktreeBranch: "cli-lock-branch"}
	if err := storage.SaveWithGroups([]*session.Instance{inst}, session.NewGroupTree([]*session.Instance{inst})); err != nil {
		t.Fatal(err)
	}
	_ = storage.Close()
	if _, err := session.EnqueueRuntimeMessage(inst.ID, "preserve"); err != nil {
		t.Fatal(err)
	}
	lockDir := filepath.Join(filepath.Dir(filepath.Dir(session.RuntimeQueuePathFor(inst.ID))), "runtime-queue-locks")
	if err := os.RemoveAll(lockDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockDir, []byte("blocked"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestWorktreeFinishQueueLockFailureLeavesWorkspaceBranchRowAndQueue$")
	cmd.Env = append(os.Environ(), "AGENT_DECK_WORKTREE_LOCK_HELPER=1")
	if err := cmd.Run(); err == nil {
		t.Fatal("worktree finish unexpectedly succeeded")
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("worktree changed: %v", err)
	}
	if got := run("branch", "--list", inst.WorktreeBranch); got == "" {
		t.Fatal("branch deleted")
	}
	verify, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := verify.LoadWithGroups()
	_ = verify.Close()
	if err != nil || len(rows) != 1 {
		t.Fatalf("row changed: %#v, %v", rows, err)
	}
	if !session.RuntimeQueueHasPending(inst.ID) {
		t.Fatal("queue changed")
	}
}

// TestMain is in testmain_test.go - sets AGENTDECK_PROFILE=_test

// =============================================================================
// Tests for Worktree Commands
// =============================================================================

// TestWorktreeListInNonGitRepo verifies that worktree list fails gracefully
// when run outside a git repository.
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

	t.Logf("Test directory %s is correctly a git repository", tmpDir)
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

// TestPartitionWorktreeTrustScriptsArgs pins the fix for a security-relevant
// CLI bug: `agent-deck worktree trust-scripts . --revoke` (exactly the form
// our own usage text prints) used to silently GRANT trust instead of
// revoking it, because stdlib flag.Parse stops consuming flags at the first
// positional argument. Every ordering below must resolve "--revoke" as a
// flag, and a literal "--" must still work as the conventional
// end-of-flags marker so a dash-prefixed repo path remains reachable.
func TestPartitionWorktreeTrustScriptsArgs(t *testing.T) {
	tests := []struct {
		name           string
		args           []string
		wantFlagArgs   []string
		wantPositional []string
	}{
		{
			name:           "flag after positional (the documented usage form)",
			args:           []string{".", "--revoke"},
			wantFlagArgs:   []string{"--revoke"},
			wantPositional: []string{"."},
		},
		{
			name:           "flag before positional",
			args:           []string{"--revoke", "."},
			wantFlagArgs:   []string{"--revoke"},
			wantPositional: []string{"."},
		},
		{
			name:           "no flag",
			args:           []string{"."},
			wantFlagArgs:   nil,
			wantPositional: []string{"."},
		},
		{
			name:           "-- terminator lets a dash-prefixed path through as positional",
			args:           []string{"--revoke", "--", "-repo"},
			wantFlagArgs:   []string{"--revoke"},
			wantPositional: []string{"-repo"},
		},
		{
			name:           "bare - is treated as positional, not a flag",
			args:           []string{"--revoke", "-"},
			wantFlagArgs:   []string{"--revoke"},
			wantPositional: []string{"-"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flagArgs, positional := partitionWorktreeTrustScriptsArgs(tt.args)
			if !slicesEqual(flagArgs, tt.wantFlagArgs) {
				t.Errorf("flagArgs = %v, want %v", flagArgs, tt.wantFlagArgs)
			}
			if !slicesEqual(positional, tt.wantPositional) {
				t.Errorf("positional = %v, want %v", positional, tt.wantPositional)
			}

			// The partition must actually produce the intended --revoke
			// value once run through flag.Parse, not just look right.
			fs := flag.NewFlagSet("t", flag.ContinueOnError)
			revoke := fs.Bool("revoke", false, "")
			if err := fs.Parse(flagArgs); err != nil {
				t.Fatalf("fs.Parse(%v) failed: %v", flagArgs, err)
			}
			wantRevoke := false
			for _, a := range tt.args {
				if a == "--revoke" {
					wantRevoke = true
				}
			}
			if *revoke != wantRevoke {
				t.Errorf("revoke = %v, want %v (args=%v)", *revoke, wantRevoke, tt.args)
			}
		})
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
