package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestUniqueForkBranch_BumpsOnCollision guards the PR #1299 review P1: repeated
// quick forks of the same source must not collide on the deterministic branch
// name — they bump to fork/<slug>-2, -3, … (reuses gitMustUI from
// fork_state_submit_test.go).
func TestUniqueForkBranch_BumpsOnCollision(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	gitMustUI(t, repo, "init", "-q")
	gitMustUI(t, repo, "config", "user.email", "t@example.com")
	gitMustUI(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "seed"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitMustUI(t, repo, "add", ".")
	gitMustUI(t, repo, "commit", "-qm", "seed")

	// No collision → base unchanged.
	if got := uniqueForkBranch(repo, "fork/feat"); got != "fork/feat" {
		t.Fatalf("no collision: got %q, want fork/feat", got)
	}

	// fork/feat exists → bump to -2.
	gitMustUI(t, repo, "branch", "fork/feat")
	if got := uniqueForkBranch(repo, "fork/feat"); got != "fork/feat-2" {
		t.Fatalf("one collision: got %q, want fork/feat-2", got)
	}

	// fork/feat and fork/feat-2 exist → bump to -3.
	gitMustUI(t, repo, "branch", "fork/feat-2")
	if got := uniqueForkBranch(repo, "fork/feat"); got != "fork/feat-3" {
		t.Fatalf("two collisions: got %q, want fork/feat-3", got)
	}

	// A linked worktree on the branch also counts as taken.
	wt := filepath.Join(t.TempDir(), "wt")
	gitMustUI(t, repo, "worktree", "add", "-q", "-b", "fork/wtonly", wt)
	if got := uniqueForkBranch(repo, "fork/wtonly"); got != "fork/wtonly-2" {
		t.Fatalf("worktree collision: got %q, want fork/wtonly-2", got)
	}

	// Non-git path → base unchanged (no branches to collide with).
	if got := uniqueForkBranch(t.TempDir(), "fork/feat"); got != "fork/feat" {
		t.Fatalf("non-git: got %q, want fork/feat", got)
	}
}
