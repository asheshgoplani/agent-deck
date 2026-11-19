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

// The web "Finish worktree" action mirrors `agent-deck worktree finish`; it
// removed a worktree and deleted its branch while another session still used
// it (F9). It now refuses with ErrWorktreeShared unless force is set.
func TestWebFinishWorktree_RefusesSharedWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Setenv("HOME", t.TempDir())
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	wt := filepath.Join(root, "wt-shared")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", repo},
		{"-C", repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
		{"-C", repo, "worktree", "add", "-q", "-b", "feat-shared", wt},
	} {
		out, err := exec.Command("git", args...).CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	h, storage := newHeadlessHomeForTest(t, "_test_web_finish_shared")
	mk := func(id, title string) *session.Instance {
		return &session.Instance{ID: id, Title: title, ProjectPath: wt, GroupPath: session.DefaultGroupPath,
			Tool: "bash", Command: "bash", Status: session.StatusStopped, CreatedAt: time.Now(),
			WorktreePath: wt, WorktreeRepoRoot: repo, WorktreeBranch: "feat-shared"}
	}
	all := []*session.Instance{mk("web-wt-1", "wt-one"), mk("web-wt-4", "wt-four")}
	require.NoError(t, storage.SaveWithGroups(all, session.NewGroupTree(all)))

	m := NewWebMutator(h)
	_, err := m.FinishWorktree("web-wt-1", menudata.WorktreeFinishOptions{NoMerge: true})
	require.Error(t, err)
	require.True(t, errors.Is(err, menudata.ErrWorktreeShared), "want ErrWorktreeShared, got %v", err)
	require.Contains(t, err.Error(), "wt-four")
	_, statErr := os.Stat(wt)
	require.NoError(t, statErr, "the refused finish must keep the shared worktree")
	require.NoError(t, exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet", "refs/heads/feat-shared").Run(),
		"the refused finish must keep the branch")
}
