package git

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCreateWorktree_NewBranch_BranchesFromFreshOriginMain_RegressionFor973
// pins the fix for https://github.com/asheshgoplani/agent-deck/issues/973.
//
// Bug: a worker spawned via `agent-deck launch -w <branch> -b` created the new
// branch from whatever the caller's local HEAD pointed to. When the caller had
// checked out an old release tag (e.g. v1.7.44), the new fix-branch was
// silently rooted there, and the worker's PR included every change between
// that tag and current main — a 414-file near-miss in the real incident.
//
// Invariant pinned here: when CreateWorktree creates a *new* branch and the
// repo has an origin remote, the worktree must be rooted at freshly-fetched
// origin/<default-branch>, never at the caller's possibly-stale local HEAD.
//
// Setup mirrors the incident:
//  1. Bare origin with main at C1.
//  2. Local clone, commit C2, push so origin/main = C2 on the wire.
//  3. Rewind the local remote-tracking ref refs/remotes/origin/main to C1 to
//     simulate "local refs not fetched recently".
//  4. Detach HEAD onto an older tag (v1.0 = C1) — this is the "stale base"
//     the worker would otherwise inherit.
//  5. Call CreateWorktree for a brand-new fix branch.
//
// Assertion: the worktree HEAD equals C2 (current origin/main on the wire),
// not C1 (the stale tag the caller happened to have checked out).
func TestCreateWorktree_NewBranch_BranchesFromFreshOriginMain_RegressionFor973(t *testing.T) {
	tmp := t.TempDir()

	remoteDir := filepath.Join(tmp, "origin.git")
	if err := os.MkdirAll(remoteDir, 0o755); err != nil {
		t.Fatalf("mkdir remote: %v", err)
	}
	runGit(t, remoteDir, "init", "--bare", "-b", "main")

	localDir := filepath.Join(tmp, "local")
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatalf("mkdir local: %v", err)
	}
	runGit(t, localDir, "init", "-b", "main")
	runGit(t, localDir, "config", "user.email", "test@test.com")
	runGit(t, localDir, "config", "user.name", "Test User")
	runGit(t, localDir, "remote", "add", "origin", remoteDir)

	// C1: initial commit on main, push, tag as v1.0.
	if err := os.WriteFile(filepath.Join(localDir, "README.md"), []byte("c1"), 0o644); err != nil {
		t.Fatalf("write c1: %v", err)
	}
	runGit(t, localDir, "add", ".")
	runGit(t, localDir, "commit", "-m", "c1")
	runGit(t, localDir, "push", "-u", "origin", "main")
	runGit(t, localDir, "tag", "v1.0")
	runGit(t, localDir, "push", "origin", "v1.0")
	c1 := runGit(t, localDir, "rev-parse", "HEAD")

	// C2: advance main and push so origin/main on the wire = C2.
	if err := os.WriteFile(filepath.Join(localDir, "README.md"), []byte("c2"), 0o644); err != nil {
		t.Fatalf("write c2: %v", err)
	}
	runGit(t, localDir, "commit", "-am", "c2")
	runGit(t, localDir, "push", "origin", "main")
	c2 := runGit(t, localDir, "rev-parse", "HEAD")

	if c1 == c2 {
		t.Fatalf("test setup invalid: c1 == c2 == %s", c1)
	}

	// Simulate the incident: caller has stale local refs + detached HEAD on
	// the old tag. Both the local main branch and the remote-tracking ref
	// are rewound to C1, so without a fresh fetch a worker would root its
	// new branch at C1 (the v1.7.44-equivalent stale tag).
	runGit(t, localDir, "update-ref", "refs/heads/main", c1)
	runGit(t, localDir, "update-ref", "refs/remotes/origin/main", c1)
	runGit(t, localDir, "checkout", "--detach", "v1.0")

	if got := runGit(t, localDir, "rev-parse", "HEAD"); got != c1 {
		t.Fatalf("test setup: expected detached HEAD at v1.0=%s, got %s", c1, got)
	}
	if got := runGit(t, localDir, "rev-parse", "refs/remotes/origin/main"); got != c1 {
		t.Fatalf("test setup: expected stale remote-tracking origin/main=%s, got %s", c1, got)
	}

	// Worker spawn: create a brand-new fix branch via worktree.
	worktreePath := filepath.Join(tmp, "fix-973-wt")
	if err := CreateWorktree(localDir, worktreePath, "fix/v1.9.x-973-worker-spawn"); err != nil {
		t.Fatalf("CreateWorktree failed: %v", err)
	}

	headSHA := runGit(t, worktreePath, "rev-parse", "HEAD")
	if headSHA != c2 {
		t.Fatalf(
			"regression #973: worker worktree branched off stale ref.\n"+
				"  got  HEAD = %s\n"+
				"  want HEAD = %s (fresh origin/main on the wire)\n"+
				"  stale c1  = %s (v1.0 tag the caller had checked out)\n"+
				"  A PR opened from this worktree would carry every diff "+
				"between v1.0 and main — exactly the 414-file near-miss.",
			headSHA, c2, c1,
		)
	}
}
