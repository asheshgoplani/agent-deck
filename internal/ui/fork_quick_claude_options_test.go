package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A quick fork (f) into a new worktree must launch with the [claude] launch
// defaults when the parent persisted no Claude options of its own, exactly as
// a fork without a worktree does. buildForkCmd needs a non-nil ClaudeOptions to
// carry the worktree fields; an empty one reads downstream as "every launch
// option off" (only nil falls back to config), so the fork lost
// --dangerously-skip-permissions and --chrome, and the empty options were
// persisted on the fork record.
func TestQuickFork_WorktreeForkKeepsConfiguredClaudeOptions(t *testing.T) {
	repo := forkDefaultsGitRepo(t)
	commit := exec.Command("git", "-c", "user.name=t", "-c", "user.email=t@example.com",
		"commit", "-q", "--allow-empty", "-m", "init")
	commit.Dir = repo
	out, err := commit.CombinedOutput()
	require.NoError(t, err, string(out))

	cfg, err := session.LoadUserConfig()
	require.NoError(t, err)
	on, off := true, false
	cfg.Claude.DangerousMode = &on
	cfg.Claude.UseChrome = true
	cfg.Fork.Worktree = &on
	cfg.Fork.WithState = &off
	require.NoError(t, session.SaveUserConfig(cfg))
	session.ClearUserConfigCache()

	source := session.NewInstanceWithTool("opts parent", repo, "claude")
	source.ClaudeSessionID = "11111111-2222-3333-4444-555555555555"
	source.ClaudeDetectedAt = time.Now()
	require.Nil(t, source.GetClaudeOptions(), "precondition: the parent persisted no Claude options")

	h := &Home{forkingSessions: make(map[string]time.Time)}
	cmd := h.quickForkSession(source)
	require.NotNil(t, cmd, "quick fork must dispatch")
	msg, ok := cmd().(sessionForkedMsg)
	require.True(t, ok)
	require.NoError(t, msg.err)
	require.NotNil(t, msg.instance)
	t.Cleanup(func() { _ = msg.instance.Kill() })

	wt, err := filepath.EvalSymlinks(msg.instance.ProjectPath)
	require.NoError(t, err)
	root, err := filepath.EvalSymlinks(repo)
	require.NoError(t, err)
	require.NotEqual(t, root, wt, "precondition: the fork runs in its own worktree")
	_, err = os.Stat(filepath.Join(wt, ".git"))
	require.NoError(t, err, "precondition: the worktree was created")

	assert.Contains(t, msg.instance.Command, "--dangerously-skip-permissions",
		"worktree fork must keep [claude].dangerous_mode")
	assert.Contains(t, msg.instance.Command, "--chrome",
		"worktree fork must keep [claude].use_chrome")
	persisted := msg.instance.GetClaudeOptions()
	require.NotNil(t, persisted)
	assert.True(t, persisted.SkipPermissions,
		"the fork record must not persist launch options with everything off")
}
