package git

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorktree_MergeBack_BareRepo_RegressionFor891 reproduces issue #891.
//
// Reporter (@Clindbergh) starts a session in a bare-repo layout (see #715):
//
//	project/
//	├── .bare/         <-- bare git dir
//	└── worktreeN/     <-- linked worktree, .git is a file
//
// Closing the session with `w` to merge the worktree branch back into main
// fails because the merge step shells out `git -C <projectRoot> checkout` —
// projectRoot is not a git working tree, and the bare dir has none either,
// so checkout exits 128 with "not a git repository" / "must be run in a
// work tree". The merge never happens and the worktree is left orphaned.
//
// This test asserts that merge-back succeeds in the bare-repo layout: after
// MergeBack, the target branch must point at the feature branch's tip.
func TestWorktree_MergeBack_BareRepo_RegressionFor891(t *testing.T) {
	projectRoot, bareDir, worktrees := createBareRepoLayout(t, "main-wt", "feature-wt")
	featureWT := worktrees[1]
	featureBranch := "feature-feature-wt"

	// Make a commit on the feature branch (fast-forward case from main).
	runIn(t, featureWT, "config", "user.email", "test@test.com")
	runIn(t, featureWT, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(featureWT, "feature.txt"), []byte("from feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runIn(t, featureWT, "add", "feature.txt")
	runIn(t, featureWT, "commit", "-m", "add feature.txt")

	featureSHA := revParse(t, featureWT, "HEAD")
	mainSHABefore := revParse(t, bareDir, "main")
	if featureSHA == mainSHABefore {
		t.Fatalf("test setup error: feature SHA == main SHA (%s)", featureSHA)
	}

	// MergeBack is the new bare-aware helper. It must accept the project
	// root (parent of .bare/), the source branch (feature), and the target
	// branch (main), and successfully advance main to point at feature.
	if err := MergeBack(projectRoot, featureBranch, "main"); err != nil {
		t.Fatalf("MergeBack failed in bare-repo layout: %v", err)
	}

	mainSHAAfter := revParse(t, bareDir, "main")
	if mainSHAAfter != featureSHA {
		t.Errorf("after MergeBack, main = %s, want %s (feature HEAD)", mainSHAAfter, featureSHA)
	}
}

func runIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s in %s: %v (stderr: %s)", strings.Join(args, " "), dir, err, stderr.String())
	}
}

func revParse(t *testing.T, dir, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "rev-parse", ref)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse %s in %s: %v", ref, dir, err)
	}
	return strings.TrimSpace(string(out))
}
