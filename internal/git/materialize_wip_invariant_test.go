package git

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMaterializeWipFromParent_ParentUntouched is a regression test that
// pins MaterializeWipFromParent's parent-read-only contract: after the call,
// parent's git status, staged diff, unstaged diff, and stash list must be
// byte-identical to their pre-call state. Catches future regressions where
// someone changes the materialization to use git stash, git add, or any
// other parent-mutating operation.
func TestMaterializeWipFromParent_ParentUntouched(t *testing.T) {
	parent := t.TempDir()
	createTestRepo(t, parent)

	// Build a complex WIP state on parent: staged + unstaged + untracked.
	// tracked.txt is committed first, then re-staged with edits, then has
	// additional unstaged edits on top of the staged version. Plus a new
	// untracked file.
	if err := os.WriteFile(filepath.Join(parent, "tracked.txt"), []byte("tracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, parent, "add", "tracked.txt")
	runGit(t, parent, "commit", "-m", "tracked")

	if err := os.WriteFile(filepath.Join(parent, "tracked.txt"), []byte("staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, parent, "add", "tracked.txt")

	if err := os.WriteFile(filepath.Join(parent, "tracked.txt"), []byte("staged\nunstaged\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(parent, "new-untracked.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	statusBefore := runGit(t, parent, "status", "--porcelain")
	diffCachedBefore := runGit(t, parent, "diff", "--cached")
	diffBefore := runGit(t, parent, "diff")
	stashBefore := runGit(t, parent, "stash", "list")

	parentHead, err := HeadCommit(parent)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}

	child := parent + "-fork"
	if _, err := CreateWorktreeAtStartPoint(parent, child, "fork/inv", parentHead); err != nil {
		t.Fatalf("CreateWorktreeAtStartPoint: %v", err)
	}

	if err := MaterializeWipFromParent(parent, child, false); err != nil {
		t.Fatalf("MaterializeWipFromParent: %v", err)
	}

	if got := runGit(t, parent, "status", "--porcelain"); got != statusBefore {
		t.Fatalf("parent status changed:\nbefore:\n%s\nafter:\n%s", statusBefore, got)
	}
	if got := runGit(t, parent, "diff", "--cached"); got != diffCachedBefore {
		t.Fatalf("parent staged diff changed:\nbefore:\n%s\nafter:\n%s", diffCachedBefore, got)
	}
	if got := runGit(t, parent, "diff"); got != diffBefore {
		t.Fatalf("parent unstaged diff changed:\nbefore:\n%s\nafter:\n%s", diffBefore, got)
	}
	if got := runGit(t, parent, "stash", "list"); got != stashBefore {
		t.Fatalf("parent stash list changed:\nbefore:\n%s\nafter:\n%s", stashBefore, got)
	}
}
