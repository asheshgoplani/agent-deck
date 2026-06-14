package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Issue #1449: finishing ONE of several sessions that share a single worktree
// removed the shared worktree directory AND deleted the branch, stranding the
// remaining sessions (their `worktree info` flipped to MISSING).
//
// RemoveSessionWorktree had the #1200 "don't delete the original repo" guard
// but NO "another live session still references this worktree" guard. The fix
// adds a shared-use check: when other live instances resolve (via EvalSymlinks)
// to the same worktree directory, the destructive git steps (worktree dir
// removal + branch delete) are skipped and only THIS session's record is
// dropped (detach). When the LAST sharer is finished, normal cleanup runs.

func sharedWtBranchExists(t *testing.T, repo, branch string) bool {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return cmd.Run() == nil
}

// TestOtherSessionsShareWorktree reports the shared-use predicate directly.
func TestOtherSessionsShareWorktree(t *testing.T) {
	repo := issue1200InitRepo(t)
	wt := issue1200AddWorktree(t, repo, "shared-feat")
	otherWt := issue1200AddWorktree(t, repo, "lonely-feat")

	target := &Instance{ID: "a", WorktreePath: wt, WorktreeRepoRoot: repo}
	sameWt := &Instance{ID: "b", WorktreePath: wt, WorktreeRepoRoot: repo}
	diffWt := &Instance{ID: "c", WorktreePath: otherWt, WorktreeRepoRoot: repo}

	cases := []struct {
		name   string
		others []*Instance
		want   bool
	}{
		{"no others", nil, false},
		{"only itself in others (same ID excluded)", []*Instance{target}, false},
		{"another session on the same worktree", []*Instance{sameWt}, true},
		{"another session on a different worktree", []*Instance{diffWt}, false},
		{"mixed: one shares, one does not", []*Instance{diffWt, sameWt}, true},
		{"nil entries are skipped", []*Instance{nil, diffWt}, false},
		{"non-worktree entries are skipped", []*Instance{{ID: "d"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := OtherSessionsShareWorktree(target, tc.others); got != tc.want {
				t.Errorf("OtherSessionsShareWorktree = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestOtherSessionsShareWorktree_SymlinkAlias proves the EvalSymlinks resolve:
// an alias path pointing at the same worktree dir counts as a sharer, so a
// symlinked sibling is not silently treated as unrelated and then stranded.
func TestOtherSessionsShareWorktree_SymlinkAlias(t *testing.T) {
	repo := issue1200InitRepo(t)
	wt := issue1200AddWorktree(t, repo, "alias-feat")

	aliasDir := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(wt, aliasDir); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	target := &Instance{ID: "a", WorktreePath: wt, WorktreeRepoRoot: repo}
	aliased := &Instance{ID: "b", WorktreePath: aliasDir, WorktreeRepoRoot: repo}

	if !OtherSessionsShareWorktree(target, []*Instance{aliased}) {
		t.Fatalf("a symlink alias to the same worktree must count as a sharer")
	}
}

// TestRemoveSessionWorktreeUnlessShared_NonLastDetaches is THE #1449 regression:
// finishing a non-last session sharing a worktree must NOT remove the worktree
// dir nor delete the branch — the remaining sibling stays intact.
func TestRemoveSessionWorktreeUnlessShared_NonLastDetaches(t *testing.T) {
	repo := issue1200InitRepo(t)
	wt := issue1200AddWorktree(t, repo, "shared")

	target := &Instance{ID: "a", WorktreePath: wt, WorktreeRepoRoot: repo, WorktreeBranch: "shared"}
	sibling := &Instance{ID: "b", WorktreePath: wt, WorktreeRepoRoot: repo, WorktreeBranch: "shared"}

	removed, err := RemoveSessionWorktreeUnlessShared(target, []*Instance{sibling})
	if err != nil {
		t.Fatalf("RemoveSessionWorktreeUnlessShared (non-last) error: %v", err)
	}
	if removed {
		t.Fatalf("non-last sharer must NOT remove the shared worktree (got removed=true)")
	}
	if _, statErr := os.Stat(wt); os.IsNotExist(statErr) {
		t.Fatalf("DATA LOSS (#1449): shared worktree dir deleted while a sibling session still uses it")
	}
	if !sharedWtBranchExists(t, repo, "shared") {
		t.Fatalf("DATA LOSS (#1449): branch deleted while a sibling session still uses the worktree")
	}
}

// TestRemoveSessionWorktreeUnlessShared_LastCleansUp proves the cleanup still
// runs for the final sharer: once no other session references the worktree,
// finishing it removes the dir as before.
func TestRemoveSessionWorktreeUnlessShared_LastCleansUp(t *testing.T) {
	repo := issue1200InitRepo(t)
	wt := issue1200AddWorktree(t, repo, "lastone")
	sentinel := filepath.Join(repo, "PRECIOUS.txt")
	if err := os.WriteFile(sentinel, []byte("main repo work"), 0o644); err != nil {
		t.Fatalf("seed sentinel: %v", err)
	}

	target := &Instance{ID: "a", WorktreePath: wt, WorktreeRepoRoot: repo, WorktreeBranch: "lastone"}

	removed, err := RemoveSessionWorktreeUnlessShared(target, nil)
	if err != nil {
		t.Fatalf("RemoveSessionWorktreeUnlessShared (last) error: %v", err)
	}
	if !removed {
		t.Fatalf("last sharer must remove its dedicated worktree (got removed=false)")
	}
	if _, statErr := os.Stat(wt); !os.IsNotExist(statErr) {
		t.Fatalf("expected the dedicated worktree %s to be removed for the last sharer", wt)
	}
	if _, statErr := os.Stat(sentinel); os.IsNotExist(statErr) {
		t.Fatalf("DATA LOSS: original repo removed during last-sharer cleanup")
	}
}

// TestRemoveSessionWorktreeUnlessShared_ReuseStillGuarded keeps the #1200
// guarantee intact under the new entry point: a reused repo is never removed,
// regardless of other sessions.
func TestRemoveSessionWorktreeUnlessShared_ReuseStillGuarded(t *testing.T) {
	repo := issue1200InitRepo(t)
	sentinel := filepath.Join(repo, "PRECIOUS_USER_WORK.txt")
	if err := os.WriteFile(sentinel, []byte("uncommitted work"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	inst := &Instance{ID: "a", WorktreePath: repo, WorktreeRepoRoot: repo}
	removed, err := RemoveSessionWorktreeUnlessShared(inst, nil)
	if err != nil {
		t.Fatalf("RemoveSessionWorktreeUnlessShared (reuse) error: %v", err)
	}
	if removed {
		t.Fatalf("reused repo must NOT be removed (#1200)")
	}
	if _, statErr := os.Stat(sentinel); os.IsNotExist(statErr) {
		t.Fatalf("DATA LOSS (#1200): reused repo deleted")
	}
}
