package ui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asheshgoplani/agent-deck/internal/menudata"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The TUI delete, the TUI finish and the web finish only counted users of a
// worktree in the profile on screen. A session in another local profile at
// the same worktree was left at a path that no longer exists.

// crossProfileWorktreeFixture makes a repo with one linked worktree, a Home
// whose only session uses it, and a session in another local profile at the
// same worktree.
func crossProfileWorktreeFixture(t *testing.T, homeProfile string) (h *Home, inst *session.Instance, wt, repo string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Setenv("HOME", t.TempDir())
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	root := t.TempDir()
	repo = filepath.Join(root, "repo")
	wt = filepath.Join(root, "wt-shared")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", repo},
		{"-C", repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
		{"-C", repo, "worktree", "add", "-q", "-b", "feat-shared", wt},
	} {
		out, err := exec.Command("git", args...).CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	h, storage := newHeadlessHomeForTest(t, homeProfile)
	inst = &session.Instance{ID: "xp-wt-1", Title: "wt-one", ProjectPath: wt, GroupPath: session.DefaultGroupPath,
		Tool: "bash", Command: "bash", Status: session.StatusStopped, CreatedAt: time.Now(),
		WorktreePath: wt, WorktreeRepoRoot: repo, WorktreeBranch: "feat-shared"}
	mine := []*session.Instance{inst}
	require.NoError(t, storage.SaveWithGroups(mine, session.NewGroupTree(mine)))
	h.instances = mine
	h.instanceByID[inst.ID] = inst

	other, err := session.NewStorageWithProfile("other_ui_profile_test")
	require.NoError(t, err)
	theirs := []*session.Instance{{ID: "xp-other", Title: "wt-other-profile", ProjectPath: wt,
		GroupPath: session.DefaultGroupPath, Tool: "bash", Command: "bash", Status: session.StatusStopped, CreatedAt: time.Now()}}
	require.NoError(t, other.SaveWithGroups(theirs, session.NewGroupTree(theirs)))
	require.NoError(t, other.Close())
	return h, inst, wt, repo
}

func requireWorktreeAndBranch(t *testing.T, wt, repo, what string) {
	t.Helper()
	_, statErr := os.Stat(wt)
	require.NoError(t, statErr, "%s removed a worktree a session in another profile uses", what)
	require.NoError(t, exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet", "refs/heads/feat-shared").Run(),
		"%s deleted the branch of a worktree another profile uses", what)
}

func TestWebFinishWorktree_RefusesWorktreeUsedByOtherProfile(t *testing.T) {
	h, inst, wt, repo := crossProfileWorktreeFixture(t, "_test_xp_web_finish")
	_, err := NewWebMutator(h).FinishWorktree(inst.ID, menudata.WorktreeFinishOptions{NoMerge: true})
	require.Error(t, err)
	require.True(t, errors.Is(err, menudata.ErrWorktreeShared), "want ErrWorktreeShared, got %v", err)
	require.Contains(t, err.Error(), "wt-other-profile")
	requireWorktreeAndBranch(t, wt, repo, "web finish")
}

func TestTUIDelete_KeepsWorktreeUsedByOtherProfile(t *testing.T) {
	h, inst, wt, repo := crossProfileWorktreeFixture(t, "_test_xp_tui_delete")
	_ = h.deleteSession(inst)()
	requireWorktreeAndBranch(t, wt, repo, "TUI delete")
}

func TestTUIFinish_KeepsWorktreeUsedByOtherProfile(t *testing.T) {
	h, inst, wt, repo := crossProfileWorktreeFixture(t, "_test_xp_tui_finish")
	// The dialog found no sharer in this profile (shared=false).
	_ = h.finishWorktree(inst, inst.ID, inst.Title, "feat-shared", repo, wt, false, "", false, false)()
	requireWorktreeAndBranch(t, wt, repo, "TUI finish")
}
