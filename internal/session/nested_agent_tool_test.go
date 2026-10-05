package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Detection reports what is running. It must not rewrite the configured tool
// Restart launches. A shell-created session that was previously labeled claude
// stays claude for restart when the only sample is a child shell, and a nested
// OpenCode is observed without becoming the launch identity.
func TestUpdateStatus_ObservedRuntimeDoesNotRewriteLaunchTool(t *testing.T) {
	skipIfNoTmuxBinary(t)

	inst := NewInstance("nested-agent-launch-identity", t.TempDir())
	inst.Tool = "shell"
	inst.Command = "sleep 60"
	require.NoError(t, inst.Start())
	t.Cleanup(func() { _ = inst.Kill() })

	time.Sleep(2 * time.Second)
	tmux.ExpireStartupWindowForTest(t, inst.tmuxSession)

	t.Run("child shell does not demote a persisted agent", func(t *testing.T) {
		inst.SetToolThreadSafe("claude")
		inst.tmuxSession.SetAgentTreeForTest(t, 10, []tmux.PaneProcess{
			{PID: 10, PPID: 1, Comm: "bash"},
		})
		tmux.SeedPaneInfoCacheForTest(t, map[string]tmux.PaneInfo{
			inst.tmuxSession.Name: {CurrentCommand: "bash"},
		})

		require.NoError(t, inst.UpdateStatus())
		assert.Equal(t, "claude", inst.GetToolThreadSafe(),
			"launch identity must survive a shell sample with no agent in the tree")
		assert.Equal(t, "shell", inst.ObservedTool())
		assert.Equal(t, "claude", inst.DisplayToolThreadSafe(),
			"a shell observation must not hide the configured agent")
	})

	t.Run("nested opencode is observed and does not become the launch tool", func(t *testing.T) {
		inst.SetToolThreadSafe("shell")
		tmux.ExpireToolDetectionForTest(inst.tmuxSession)
		inst.tmuxSession.SetAgentTreeForTest(t, 10, []tmux.PaneProcess{
			{PID: 10, PPID: 1, Comm: "bash"},
			{PID: 11, PPID: 10, Comm: "nvim"},
			{PID: 12, PPID: 11, Comm: "opencode", StartID: "42"},
		})
		tmux.SeedPaneInfoCacheForTest(t, map[string]tmux.PaneInfo{
			inst.tmuxSession.Name: {CurrentCommand: "nvim"},
		})

		require.NoError(t, inst.UpdateStatus())
		assert.Equal(t, "shell", inst.GetToolThreadSafe(),
			"restart must still launch the configured shell, not opencode")
		assert.Equal(t, "opencode", inst.ObservedTool())
		assert.Equal(t, "opencode", inst.DisplayToolThreadSafe())
	})
}
