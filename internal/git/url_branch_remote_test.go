package git

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCreateWorktree_NewBranch_URLInBranchRemoteConfig pins the fix for
// worktree creation failing with "fatal: invalid reference: <url>/main".
//
// Bug: git permits `branch.<name>.remote` to hold a bare URL instead of a
// remote name (this is what `git pull <fork-url> <branch>` leaves behind when
// checking out a PR branch directly from a fork). getDefaultRemote returned
// that URL verbatim, and freshOriginDefaultBranchRef happily fetched from it
// (fetching by URL is valid git), then handed callers "<url>/<default>" as a
// base ref — which is not a ref at all, so `git worktree add` died with
// "invalid reference" and session creation failed.
//
// Invariant pinned here: a URL in branch.<name>.remote must not be treated as
// the default remote; resolution falls through to the configured "origin",
// and a brand-new branch worktree is rooted at fresh origin/<default>.
func TestCreateWorktree_NewBranch_URLInBranchRemoteConfig(t *testing.T) {
	tmp := t.TempDir()

	remoteDir := filepath.Join(tmp, "origin.git")
	if err := os.MkdirAll(remoteDir, 0o755); err != nil {
		t.Fatalf("mkdir remote: %v", err)
	}
	runGit(t, remoteDir, "init", "--bare", "-b", "main")

	// A second bare repo standing in for the fork URL. It must be fetchable:
	// in the real incident `git fetch <url> main` succeeded, which is exactly
	// why the broken "<url>/main" base ref made it through to worktree add.
	forkDir := filepath.Join(tmp, "fork.git")
	if err := os.MkdirAll(forkDir, 0o755); err != nil {
		t.Fatalf("mkdir fork: %v", err)
	}
	runGit(t, forkDir, "init", "--bare", "-b", "main")

	localDir := filepath.Join(tmp, "local")
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatalf("mkdir local: %v", err)
	}
	runGit(t, localDir, "init", "-b", "main")
	runGit(t, localDir, "config", "user.email", "test@test.com")
	runGit(t, localDir, "config", "user.name", "Test User")
	runGit(t, localDir, "remote", "add", "origin", remoteDir)

	// C1 on main, pushed to both origin and the fork.
	if err := os.WriteFile(filepath.Join(localDir, "README.md"), []byte("c1"), 0o644); err != nil {
		t.Fatalf("write c1: %v", err)
	}
	runGit(t, localDir, "add", ".")
	runGit(t, localDir, "commit", "-m", "c1")
	runGit(t, localDir, "push", "-u", "origin", "main")
	runGit(t, localDir, "push", forkDir, "main")

	// C2 advances origin/main only — lets the assertion distinguish "rooted
	// at fresh origin/main" from "rooted at the fork or stale local state".
	if err := os.WriteFile(filepath.Join(localDir, "README.md"), []byte("c2"), 0o644); err != nil {
		t.Fatalf("write c2: %v", err)
	}
	runGit(t, localDir, "commit", "-am", "c2")
	runGit(t, localDir, "push", "origin", "main")
	c2 := runGit(t, localDir, "rev-parse", "HEAD")

	// The incident state: current branch's remote config is a URL, not a
	// remote name.
	runGit(t, localDir, "checkout", "-b", "pr-from-fork")
	runGit(t, localDir, "config", "branch.pr-from-fork.remote", forkDir)

	worktreePath := filepath.Join(tmp, "new-feature-wt")
	if err := CreateWorktree(localDir, worktreePath, "feature/new-thing"); err != nil {
		t.Fatalf("CreateWorktree failed (URL in branch.<name>.remote leaked into base ref?): %v", err)
	}

	headSHA := runGit(t, worktreePath, "rev-parse", "HEAD")
	if headSHA != c2 {
		t.Fatalf("worktree not rooted at fresh origin/main: got HEAD %s, want %s", headSHA, c2)
	}
}
