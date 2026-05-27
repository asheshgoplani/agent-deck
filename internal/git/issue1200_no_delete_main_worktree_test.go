package git

import (
	"os"
	"path/filepath"
	"testing"
)

// Issue #1200 (reported by @mic-web): dismissing a session created with
// worktree_reuse permanently deleted the user's ORIGINAL repository.
//
// When the repo was already on the desired branch, agent-deck created no
// dedicated worktree and the session's WorktreePath pointed at the real repo
// root. On dismiss, RemoveWorktree(repoRoot, repoRoot, force=true) ran
// `git worktree remove --force <repoRoot>` (which fails on a main working
// tree), then the force-mode fallback ran os.RemoveAll(repoRoot) — destroying
// the repository (uncommitted changes, stashes, local branches), silently and
// with no undo.
//
// These tests pin the git-layer guard: the force fallback must NEVER
// os.RemoveAll a path that is not a LINKED worktree (the main working tree, or
// any non-worktree path). Legitimate linked-worktree cleanup must still work.

// TestRemoveWorktree_RefusesToDeleteMainWorktree_Issue1200 is THE critical
// data-loss regression. It must FAIL on pre-fix code (the repo is deleted) and
// PASS after the fix (the repo survives).
func TestRemoveWorktree_RefusesToDeleteMainWorktree_Issue1200(t *testing.T) {
	repo := t.TempDir()
	createTestRepo(t, repo)

	// Sentinel standing in for the user's irreplaceable work. If this file
	// disappears, the repository was destroyed.
	sentinel := filepath.Join(repo, "PRECIOUS_USER_WORK.txt")
	if err := os.WriteFile(sentinel, []byte("uncommitted work, stashes, branches"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	// Reproduce worktree_reuse: worktreePath == repoRoot (the main working tree).
	err := RemoveWorktree(repo, repo, true)

	if _, statErr := os.Stat(sentinel); os.IsNotExist(statErr) {
		t.Fatalf("DATA LOSS (#1200): RemoveWorktree deleted the original repository — sentinel %s is gone", sentinel)
	}
	if _, statErr := os.Stat(filepath.Join(repo, ".git")); os.IsNotExist(statErr) {
		t.Fatalf("DATA LOSS (#1200): RemoveWorktree deleted the repository .git directory")
	}
	if err == nil {
		t.Fatalf("expected RemoveWorktree to refuse removing the main working tree, got nil error")
	}
}

// TestRemoveWorktree_RefusesNonWorktreePath_Issue1200 covers the paranoid edge:
// a force removal aimed at a plain directory that is not a git worktree at all
// must also be refused rather than blindly os.RemoveAll'd.
func TestRemoveWorktree_RefusesNonWorktreePath_Issue1200(t *testing.T) {
	repo := t.TempDir()
	createTestRepo(t, repo)

	// A plain directory living outside any worktree of repo.
	outside := filepath.Join(t.TempDir(), "not-a-worktree")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	keep := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(keep, []byte("do not delete"), 0o644); err != nil {
		t.Fatalf("write keep: %v", err)
	}

	err := RemoveWorktree(repo, outside, true)

	if _, statErr := os.Stat(keep); os.IsNotExist(statErr) {
		t.Fatalf("DATA LOSS (#1200): RemoveWorktree deleted a non-worktree directory %s", outside)
	}
	if err == nil {
		t.Fatalf("expected RemoveWorktree to refuse removing a non-worktree path, got nil error")
	}
}

// TestRemoveWorktree_StillRemovesLinkedWorktree_Issue1200 guards against
// over-correction: a genuine agent-deck-created linked worktree (whose
// `git worktree remove --force` fails due to untracked files, forcing the
// os.RemoveAll fallback) must still be removed, and the main repo must survive.
func TestRemoveWorktree_StillRemovesLinkedWorktree_Issue1200(t *testing.T) {
	repo := t.TempDir()
	createTestRepo(t, repo)
	sentinel := filepath.Join(repo, "PRECIOUS_USER_WORK.txt")
	if err := os.WriteFile(sentinel, []byte("main repo work"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	wt := filepath.Join(t.TempDir(), "linked-wt")
	if err := CreateWorktree(repo, wt, "feature-x"); err != nil {
		t.Fatalf("create linked worktree: %v", err)
	}
	// Untracked file makes `git worktree remove --force` exit non-zero,
	// exercising the os.RemoveAll fallback for a legitimate worktree.
	if err := os.WriteFile(filepath.Join(wt, "untracked.txt"), []byte("y"), 0o644); err != nil {
		t.Fatalf("write untracked: %v", err)
	}

	if err := RemoveWorktree(repo, wt, true); err != nil {
		t.Fatalf("expected linked worktree removal to succeed, got: %v", err)
	}
	if _, statErr := os.Stat(wt); !os.IsNotExist(statErr) {
		t.Fatalf("expected linked worktree dir %s to be removed", wt)
	}
	if _, statErr := os.Stat(sentinel); os.IsNotExist(statErr) {
		t.Fatalf("DATA LOSS (#1200): main repo sentinel removed during linked-worktree cleanup")
	}
}

// TestIsLinkedWorktree_Issue1200 pins the helper that distinguishes a linked
// (agent-deck-managed) worktree from the main working tree and non-repo dirs.
func TestIsLinkedWorktree_Issue1200(t *testing.T) {
	repo := t.TempDir()
	createTestRepo(t, repo)

	if IsLinkedWorktree(repo) {
		t.Errorf("main working tree must NOT be reported as a linked worktree")
	}

	wt := filepath.Join(t.TempDir(), "wt")
	if err := CreateWorktree(repo, wt, "feat"); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	if !IsLinkedWorktree(wt) {
		t.Errorf("linked worktree must be reported as a linked worktree")
	}

	if IsLinkedWorktree(t.TempDir()) {
		t.Errorf("non-repo directory must NOT be reported as a linked worktree")
	}
}
