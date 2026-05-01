package git

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// createSubmoduleLayout builds a superproject containing one initialized submodule.
//
//	super/
//	├── .git/
//	│   └── modules/erp/      ← submodule gitdir; agent-deck must NOT treat as a worktree
//	├── erp/                  ← submodule working tree (the path agent-deck should store)
//	└── README.md
//
// Returns (superprojectRoot, submoduleWorkingTree). A local file:// remote backs the
// submodule so no network is required.
func createSubmoduleLayout(t *testing.T) (super, submodule string) {
	t.Helper()

	run := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = testutil.CleanGitEnv(os.Environ())
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("git -C %s %s: %v (stderr: %s)", dir, strings.Join(args, " "), err, stderr.String())
		}
	}

	parent := t.TempDir()
	remote := filepath.Join(parent, "remote")
	if err := os.Mkdir(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	run(remote, "-c", "init.defaultBranch=main", "init")
	run(remote, "config", "user.email", "test@test.com")
	run(remote, "config", "user.name", "Test User")
	run(remote, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(remote, "README.md"), []byte("# erp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(remote, "add", ".")
	run(remote, "commit", "-m", "init")

	super = filepath.Join(parent, "super")
	if err := os.Mkdir(super, 0o755); err != nil {
		t.Fatal(err)
	}
	run(super, "-c", "init.defaultBranch=main", "init")
	run(super, "config", "user.email", "test@test.com")
	run(super, "config", "user.name", "Test User")
	run(super, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(super, "README.md"), []byte("# super\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(super, "add", ".")
	run(super, "commit", "-m", "init")

	// Modern git refuses file:// submodule fetches without protocol.file.allow=always.
	run(super, "-c", "protocol.file.allow=always", "submodule", "add", remote, "erp")
	run(super, "commit", "-m", "add erp submodule")

	submodule = filepath.Join(super, "erp")
	return super, submodule
}

// TestListWorktrees_SubmoduleReturnsWorkingTreeNotGitdir is the regression test
// for the path-normalization bug: `git worktree list --porcelain` from inside a
// submodule reports the submodule's gitdir (`<super>/.git/modules/<name>`) as
// the worktree path, not the actual working tree. parseWorktreeList must
// normalize that back to the working tree, otherwise downstream code stores
// the gitdir as ProjectPath / worktree cwd.
func TestListWorktrees_SubmoduleReturnsWorkingTreeNotGitdir(t *testing.T) {
	_, submodule := createSubmoduleLayout(t)

	wts, err := ListWorktrees(submodule)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(wts) == 0 {
		t.Fatal("expected at least one worktree entry from a submodule")
	}

	expected, _ := filepath.EvalSymlinks(submodule)
	got, _ := filepath.EvalSymlinks(wts[0].Path)
	if got != expected {
		t.Errorf("worktree path = %q, want %q (must NOT be the gitdir)", wts[0].Path, expected)
	}
	if strings.Contains(wts[0].Path, string(filepath.Separator)+".git"+string(filepath.Separator)+"modules"+string(filepath.Separator)) {
		t.Errorf("worktree path %q still points at the submodule gitdir", wts[0].Path)
	}
}

// TestGetWorktreeForBranch_SubmoduleReturnsWorkingTree exercises the helper
// that the agent-deck worktree-reuse flow consults
// (cmd/agent-deck/main.go:1319, launch_cmd.go:216, internal/ui/home.go:7897).
// Returning the gitdir here is what caused the wrong ProjectPath / cwd / data
// loss on session deletion.
func TestGetWorktreeForBranch_SubmoduleReturnsWorkingTree(t *testing.T) {
	_, submodule := createSubmoduleLayout(t)

	branch, err := GetCurrentBranch(submodule)
	if err != nil {
		t.Fatalf("GetCurrentBranch: %v", err)
	}

	got, err := GetWorktreeForBranch(submodule, branch)
	if err != nil {
		t.Fatalf("GetWorktreeForBranch: %v", err)
	}

	expected, _ := filepath.EvalSymlinks(submodule)
	resolved, _ := filepath.EvalSymlinks(got)
	if resolved != expected {
		t.Errorf("GetWorktreeForBranch(%q, %q) = %q, want %q",
			submodule, branch, resolved, expected)
	}
}

// TestRemoveWorktree_RefusesToDeleteSubmoduleGitdir is the data-loss regression
// gate. A session created before the path-normalization fix may have its
// WorktreePath persisted as the submodule's gitdir. When the user deletes that
// session, RemoveWorktree(force=true) used to fall back to os.RemoveAll on the
// gitdir, destroying the submodule's git history. The defensive guard must
// surface an error instead.
func TestRemoveWorktree_RefusesToDeleteSubmoduleGitdir(t *testing.T) {
	super, submodule := createSubmoduleLayout(t)
	gitdir := filepath.Join(super, ".git", "modules", "erp")

	if _, err := os.Stat(gitdir); err != nil {
		t.Fatalf("gitdir should exist before RemoveWorktree (fixture bug?): %v", err)
	}

	// Mirror the call shape from internal/ui/home.go:8562 and
	// cmd/agent-deck/session_remove_cmd.go:177 — repoRoot = submodule worktree,
	// worktreePath = (mistakenly) the gitdir, force = true.
	err := RemoveWorktree(submodule, gitdir, true)
	if err == nil {
		t.Fatal("RemoveWorktree(force=true) on a gitdir must return an error, not silently delete")
	}

	if _, err := os.Stat(gitdir); err != nil {
		t.Fatalf("gitdir was destroyed by RemoveWorktree (data-loss regression): %v", err)
	}
}

// TestRemoveWorktree_RefusesToDeleteRepoGitFolder covers the broader class of
// gitdir-shaped paths that could end up on stale or buggy WorktreePath values:
// the repo's own .git folder and the .git/worktrees/<wt> admin dir. Neither
// case can be detected by `git rev-parse --show-toplevel` (the command errors
// out from inside both), so isGitDir uses path structure.
func TestRemoveWorktree_RefusesToDeleteRepoGitFolder(t *testing.T) {
	dir := t.TempDir()
	createTestRepo(t, dir)

	wtPath := filepath.Join(t.TempDir(), "wt-feat")
	if err := CreateWorktree(dir, wtPath, "feat"); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	cases := []struct {
		name string
		path string
	}{
		{"repo .git folder", filepath.Join(dir, ".git")},
		{".git/worktrees admin dir", filepath.Join(dir, ".git", "worktrees", "wt-feat")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := os.Stat(tc.path); err != nil {
				t.Fatalf("%s should exist before RemoveWorktree (fixture bug?): %v", tc.path, err)
			}
			err := RemoveWorktree(dir, tc.path, true)
			if err == nil {
				t.Fatalf("RemoveWorktree(force=true) on %q must error, not delete git internals", tc.path)
			}
			if _, err := os.Stat(tc.path); err != nil {
				t.Fatalf("%q was destroyed by RemoveWorktree (data-loss regression): %v", tc.path, err)
			}
		})
	}
}
