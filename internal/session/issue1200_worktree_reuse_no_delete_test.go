package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Issue #1200 (reported by @mic-web): dismissing a session created with
// worktree_reuse deleted the user's ORIGINAL repository.
//
// A reused session has WorktreePath pointing at the primary working tree (the
// repo the user is on), NOT at an agent-deck-created linked worktree. Routing
// dismissal through IsRemovableWorktree / RemoveSessionWorktree must never
// delete such a repo, while still cleaning up real linked worktrees.

func issue1200InitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("-c", "init.defaultBranch=main", "init")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# repo"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	run("add", ".")
	run("commit", "-m", "init")
	return dir
}

func issue1200AddWorktree(t *testing.T, repo, branch string) string {
	t.Helper()
	wt := filepath.Join(t.TempDir(), "wt-"+branch)
	cmd := exec.Command("git", "worktree", "add", "-b", branch, wt)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	return wt
}

// TestRemoveSessionWorktree_ReuseDoesNotDeleteRepo_Issue1200 is THE critical
// regression: a worktree_reuse session (WorktreePath == repo root) must NOT be
// removed on dismissal, and the original repo + its contents must survive.
func TestRemoveSessionWorktree_ReuseDoesNotDeleteRepo_Issue1200(t *testing.T) {
	repo := issue1200InitRepo(t)
	sentinel := filepath.Join(repo, "PRECIOUS_USER_WORK.txt")
	if err := os.WriteFile(sentinel, []byte("uncommitted work, stashes, branches"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	// worktree_reuse: repo already on the desired branch → path == repo root,
	// no dedicated worktree created.
	inst := &Instance{WorktreePath: repo, WorktreeRepoRoot: repo}

	removed, err := RemoveSessionWorktree(inst)
	if err != nil {
		t.Fatalf("RemoveSessionWorktree returned error for a reused repo: %v", err)
	}
	if removed {
		t.Fatalf("reused repo must NOT be removed on dismissal")
	}
	if _, statErr := os.Stat(sentinel); os.IsNotExist(statErr) {
		t.Fatalf("DATA LOSS (#1200): dismissing a reused session deleted the original repository")
	}
	if _, statErr := os.Stat(filepath.Join(repo, ".git")); os.IsNotExist(statErr) {
		t.Fatalf("DATA LOSS (#1200): dismissing a reused session deleted the repository .git directory")
	}
}

// TestRemoveSessionWorktree_RemovesDedicatedWorktree_Issue1200 guards real
// cleanup: an agent-deck-created linked worktree must still be removed, and the
// original repo must survive.
func TestRemoveSessionWorktree_RemovesDedicatedWorktree_Issue1200(t *testing.T) {
	repo := issue1200InitRepo(t)
	sentinel := filepath.Join(repo, "PRECIOUS_USER_WORK.txt")
	if err := os.WriteFile(sentinel, []byte("main repo work"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	wt := issue1200AddWorktree(t, repo, "feature-x")

	inst := &Instance{WorktreePath: wt, WorktreeRepoRoot: repo}

	removed, err := RemoveSessionWorktree(inst)
	if err != nil {
		t.Fatalf("RemoveSessionWorktree(dedicated worktree) error: %v", err)
	}
	if !removed {
		t.Fatalf("a dedicated linked worktree must be removed on dismissal")
	}
	if _, statErr := os.Stat(wt); !os.IsNotExist(statErr) {
		t.Fatalf("expected dedicated worktree %s to be removed", wt)
	}
	if _, statErr := os.Stat(sentinel); os.IsNotExist(statErr) {
		t.Fatalf("DATA LOSS (#1200): original repo removed during dedicated-worktree cleanup")
	}
}

func TestIsRemovableWorktree_Issue1200(t *testing.T) {
	repo := issue1200InitRepo(t)
	wt := issue1200AddWorktree(t, repo, "feat")

	// A plain directory that is not a worktree, but with WorktreePath set —
	// the paranoid guard must still refuse it.
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}

	cases := []struct {
		name string
		inst *Instance
		want bool
	}{
		{"nil instance", nil, false},
		{"empty metadata", &Instance{}, false},
		{"empty worktree path", &Instance{WorktreeRepoRoot: repo}, false},
		{"empty repo root", &Instance{WorktreePath: wt}, false},
		{"reused repo (path == root)", &Instance{WorktreePath: repo, WorktreeRepoRoot: repo}, false},
		{"path outside, not a worktree", &Instance{WorktreePath: outside, WorktreeRepoRoot: repo}, false},
		{"dedicated linked worktree", &Instance{WorktreePath: wt, WorktreeRepoRoot: repo}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRemovableWorktree(tc.inst); got != tc.want {
				t.Errorf("IsRemovableWorktree = %v, want %v", got, tc.want)
			}
		})
	}
}
