package ui

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/stretchr/testify/require"
)

// requireJJWithIdentity skips when jj is absent and pins a throwaway JJ_CONFIG
// supplying a commit identity, visible to both the test setup and the
// production jj invocations (which inherit os.Environ via exec.Command).
func requireJJWithIdentity(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	cfg := filepath.Join(t.TempDir(), "jjconfig.toml")
	if err := os.WriteFile(cfg, []byte("[user]\nname = \"Test User\"\nemail = \"test@example.com\"\n"), 0o644); err != nil {
		t.Fatalf("write jj config: %v", err)
	}
	t.Setenv("JJ_CONFIG", cfg)
}

func jjMustUI(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("jj", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("jj %v in %s failed: %v\n%s", args, dir, err, out)
	}
}

// setupJJRepoWithWIPUI builds a colocated jj repo with a committed base and an
// uncommitted working copy carrying a tracked edit, an untracked file, and a
// gitignored file.
func setupJJRepoWithWIPUI(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	jjMustUI(t, repo, "git", "init", "--colocate")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("base content\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("ign/\n"), 0o644))
	jjMustUI(t, repo, "describe", "-m", "base commit")
	jjMustUI(t, repo, "new", "-m", "wip")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("base content\nWIP EDIT\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("new untracked\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "ign"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "ign", "secret.env"), []byte("secret=1\n"), 0o644))
	return repo
}

func setupPureJJRepoUI(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	jjMustUI(t, repo, "git", "init")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("base content\n"), 0o644))
	jjMustUI(t, repo, "describe", "-m", "base commit")
	return repo
}

// TestForkWithStateWorkspaceJJ_CarriesParentState is the ui-level acceptance for
// #1305: forking a colocated jj repo with-state must produce a new workspace
// whose working copy holds the parent's uncommitted tracked + untracked changes,
// plus gitignored files when WithIgnored is set.
func TestForkWithStateWorkspaceJJ_CarriesParentState(t *testing.T) {
	requireJJWithIdentity(t)
	repo := setupJJRepoWithWIPUI(t)
	dest := filepath.Join(t.TempDir(), "fork")

	err := forkWithStateWorkspaceJJ(repo, repo, dest, "forkbranch",
		git.WorktreeStateOptions{WithState: true, WithIgnored: true})
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(dest, "tracked.txt"))
	require.NoError(t, err)
	require.Equal(t, "base content\nWIP EDIT\n", string(got), "tracked WIP must carry into the jj fork workspace")

	require.FileExists(t, filepath.Join(dest, "untracked.txt"), "untracked file must carry")
	require.FileExists(t, filepath.Join(dest, "ign", "secret.env"), "gitignored file must carry with WithIgnored")

	// The fork's bookmark must point at the materialized working-copy commit, not
	// the empty pre-restore commit it was created on: CreateWorkspaceAtRevision
	// sets the bookmark before MaterializeWipFromParent rewrites @, so this guards
	// that jj's rewrite-tracking moved the bookmark forward. Otherwise the working
	// copy would look correct while `forkbranch` carried none of the WIP.
	bookmarkRev := jjCommitIDUI(t, dest, "forkbranch")
	atRev := jjCommitIDUI(t, dest, "@")
	require.Equal(t, atRev, bookmarkRev, "forkbranch must resolve to the materialized @ commit")
}

// jjCommitIDUI resolves a revset to its commit id in the workspace rooted at dir.
func jjCommitIDUI(t *testing.T, dir, revset string) string {
	t.Helper()
	cmd := exec.Command("jj", "log", "-r", revset, "--no-graph", "-T", "commit_id", "-R", dir)
	// stdout only: a jj snapshot warning on stderr would otherwise corrupt the
	// parsed commit id (same hazard as resolveRevision).
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, "jj log -r %s: %s", revset, stderr.String())
	return strings.TrimSpace(string(out))
}

// TestForkWithStateWorkspaceJJ_BookmarkCollisionLeavesNoContainerDir guards the
// Option-B ordering: a destination bookmark collision must be detected BEFORE
// os.MkdirAll, so a refused fork never leaves an empty worktrees container dir
// behind. This mirrors the git path (forkWithStateWorktree validates the
// destination before deps.mkdirAll) and the jj CLI path (session_cmd.go
// BookmarkExists before its os.MkdirAll). The destination is placed under a
// not-yet-existing container so the stray-dir is observable.
func TestForkWithStateWorkspaceJJ_BookmarkCollisionLeavesNoContainerDir(t *testing.T) {
	requireJJWithIdentity(t)
	repo := setupJJRepoWithWIPUI(t)
	jjMustUI(t, repo, "bookmark", "create", "fork/taken", "-r", "@")

	container := filepath.Join(t.TempDir(), "container")
	dest := filepath.Join(container, "fork")

	err := forkWithStateWorkspaceJJ(repo, repo, dest, "fork/taken",
		git.WorktreeStateOptions{WithState: true})
	require.Error(t, err, "a colliding destination bookmark must be refused")
	require.Contains(t, err.Error(), "already exists")
	require.NoDirExists(t, container,
		"destination collision must be caught before MkdirAll — no empty container dir may be left behind")
}

// TestForkWithStateWorkspaceJJ_RejectsExistingDestination guards the
// destination-collision refusal, mirroring the git path's path-existence check.
func TestForkWithStateWorkspaceJJ_RejectsExistingDestination(t *testing.T) {
	requireJJWithIdentity(t)
	repo := setupJJRepoWithWIPUI(t)
	dest := filepath.Join(t.TempDir(), "fork")
	require.NoError(t, os.MkdirAll(dest, 0o755))

	err := forkWithStateWorkspaceJJ(repo, repo, dest, "forkbranch",
		git.WorktreeStateOptions{WithState: true})
	require.Error(t, err, "an existing destination path must be refused")
}

// TestRollbackForkWithStateWorktree_ForgetsJujutsuWorkspace verifies the
// backend-aware rollback uses jj (workspace forget + directory removal) rather
// than git on a jujutsu fork — using git here would leave the workspace
// registered and orphaned. Guards the post-create failure path of completeFork.
func TestRollbackForkWithStateWorktree_ForgetsJujutsuWorkspace(t *testing.T) {
	requireJJWithIdentity(t)
	repo := setupJJRepoWithWIPUI(t)
	dest := filepath.Join(t.TempDir(), "fork")

	err := forkWithStateWorkspaceJJ(repo, repo, dest, "forkbranch",
		git.WorktreeStateOptions{WithState: true})
	require.NoError(t, err)
	require.DirExists(t, dest)

	rollbackForkWithStateWorktree(repo, dest, "forkbranch")

	_, statErr := os.Stat(dest)
	require.True(t, os.IsNotExist(statErr), "rolled-back jj workspace directory must be removed")

	list := exec.Command("jj", "workspace", "list", "-R", repo)
	out, err := list.CombinedOutput()
	require.NoError(t, err, "jj workspace list: %s", out)
	require.NotContains(t, string(out), workspaceNameFromPathUI(dest),
		"forgotten workspace must not remain registered")
}

func TestResolveWorktreeTarget_JujutsuRepoUsesBackend(t *testing.T) {
	requireJJWithIdentity(t)
	repo := setupPureJJRepoUI(t)

	worktreePath, repoRoot, fallback, errMsg := resolveWorktreeTarget(repo, "fork/feat", false)

	require.Empty(t, errMsg)
	require.False(t, fallback, "jj repos must not fall back to a normal session")
	requireSamePath(t, repo, repoRoot)
	require.NotEmpty(t, worktreePath)
	require.Contains(t, worktreePath, "fork-feat")
}

func TestUniqueForkBranch_BumpsOnJujutsuBookmark(t *testing.T) {
	requireJJWithIdentity(t)
	repo := setupPureJJRepoUI(t)
	jjMustUI(t, repo, "bookmark", "create", "fork/feat", "-r", "@")
	jjMustUI(t, repo, "bookmark", "create", "fork/feat-2", "-r", "@")

	got := uniqueForkBranch(repo, "fork/feat")

	require.Equal(t, "fork/feat-3", got)
}

// workspaceNameFromPathUI mirrors jujutsu.workspaceNameFromPath (unexported) for
// the assertion above: the workspace name is the dir base with spaces dashed.
func workspaceNameFromPathUI(path string) string {
	return strings.ReplaceAll(filepath.Base(path), " ", "-")
}

func requireSamePath(t *testing.T, want, got string) {
	t.Helper()
	wantEval, err := filepath.EvalSymlinks(want)
	require.NoError(t, err)
	gotEval, err := filepath.EvalSymlinks(got)
	require.NoError(t, err)
	require.Equal(t, wantEval, gotEval)
}
