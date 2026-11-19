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

// A TUI delete of a multi-repo session force-removed each per-repo worktree
// without asking whether another session, in this or another local profile,
// still used it.
func TestTUIDelete_MultiRepoKeepsPerRepoWorktreeInUse(t *testing.T) {
	for _, where := range []string{"same profile", "other profile"} {
		t.Run(where, func(t *testing.T) {
			h, inst, wt, repo := crossProfileWorktreeFixture(t, "_test_xp_tui_multirepo_"+map[string]string{"same profile": "same", "other profile": "other"}[where])
			// Turn the fixture's session into a multi-repo session whose only
			// worktree is a per-repo one.
			inst.WorktreePath, inst.WorktreeRepoRoot, inst.WorktreeBranch = "", "", ""
			inst.ProjectPath = t.TempDir()
			inst.MultiRepoEnabled = true
			inst.MultiRepoWorktrees = []session.MultiRepoWorktree{{OriginalPath: repo, WorktreePath: wt, RepoRoot: repo, Branch: "feat-shared"}}
			if where == "same profile" {
				// Drop the other profile's user and add one here instead.
				dropOtherUIProfile(t)
				sib := &session.Instance{ID: "xp-sib", Title: "wt-sibling", ProjectPath: wt, GroupPath: session.DefaultGroupPath,
					Tool: "bash", Command: "bash", Status: session.StatusStopped, CreatedAt: time.Now()}
				h.instances = append(h.instances, sib)
				h.instanceByID[sib.ID] = sib
			}
			_ = h.deleteSession(inst)()
			_, statErr := os.Stat(wt)
			require.NoError(t, statErr, "TUI delete of a multi-repo session removed a per-repo worktree another session (%s) uses", where)
		})
	}
	t.Run("unused per-repo worktree is removed", func(t *testing.T) {
		h, inst, wt, repo := crossProfileWorktreeFixture(t, "_test_xp_tui_multirepo_unused")
		dropOtherUIProfile(t)
		inst.WorktreePath, inst.WorktreeRepoRoot, inst.WorktreeBranch = "", "", ""
		inst.ProjectPath = t.TempDir()
		inst.MultiRepoEnabled = true
		inst.MultiRepoWorktrees = []session.MultiRepoWorktree{{OriginalPath: repo, WorktreePath: wt, RepoRoot: repo, Branch: "feat-shared"}}
		_ = h.deleteSession(inst)()
		_, statErr := os.Stat(wt)
		require.True(t, os.IsNotExist(statErr), "TUI delete kept a per-repo worktree no other session uses (stat err %v)", statErr)
	})
}

// dropOtherUIProfile removes the fixture's other-profile session.
func dropOtherUIProfile(t *testing.T) {
	t.Helper()
	other, err := session.NewStorageWithProfile("other_ui_profile_test")
	require.NoError(t, err)
	require.NoError(t, other.DeleteInstance("xp-other"))
	require.NoError(t, other.Close())
}

// The per-repo worktrees of a multi-repo session live in its temp dir, so
// removing that dir must not take a kept per-repo worktree with it.
func TestTUIDelete_MultiRepoKeepsTempDirHoldingWorktreeInUse(t *testing.T) {
	h, inst, _, repo := crossProfileWorktreeFixture(t, "_test_xp_tui_multirepo_tmpdir")
	dropOtherUIProfile(t)
	tmp := t.TempDir()
	mrWt := filepath.Join(tmp, "repo")
	out, err := exec.Command("git", "-C", repo, "worktree", "add", "-q", "-b", "feat-mr", mrWt).CombinedOutput()
	require.NoError(t, err, "git worktree add: %s", out)
	inst.WorktreePath, inst.WorktreeRepoRoot, inst.WorktreeBranch = "", "", ""
	inst.ProjectPath = tmp
	inst.MultiRepoEnabled = true
	inst.MultiRepoTempDir = tmp
	inst.MultiRepoWorktrees = []session.MultiRepoWorktree{{OriginalPath: repo, WorktreePath: mrWt, RepoRoot: repo, Branch: "feat-mr"}}
	sib := &session.Instance{ID: "xp-sib", Title: "wt-sibling", ProjectPath: mrWt, GroupPath: session.DefaultGroupPath,
		Tool: "bash", Command: "bash", Status: session.StatusStopped, CreatedAt: time.Now()}
	h.instances = append(h.instances, sib)
	h.instanceByID[sib.ID] = sib
	_ = h.deleteSession(inst)()
	_, statErr := os.Stat(mrWt)
	require.NoError(t, statErr, "TUI delete removed the multi-repo temp dir holding a per-repo worktree another session uses")
}
