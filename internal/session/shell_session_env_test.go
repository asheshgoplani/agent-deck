package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Remote parity walk on g14 (2026-09-18), finding 3: `env | grep AGENTDECK`
// inside a shell session created on the remote printed nothing, and the
// identity file was not there to read. The same was true of a local shell
// session: a plain shell pane is the one kind of session that gets no
// command prefix, so nothing ever exported AGENTDECK_INSTANCE_ID or the
// identity file into it. It must now get what every other tool gets, and
// the file must be the one written by the agent-deck that spawned the pane.
func TestShellSession_GetsAgentDeckEnvAndIdentityFile(t *testing.T) {
	skipIfNoTmuxBinary(t)
	home := withTempAgentDeckHome(t, "")
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("AGENTDECK_ACCOUNT", "")
	ClearUserConfigCache()

	inst := NewInstanceWithTool("parity shell", t.TempDir(), "shell")
	t.Cleanup(func() { _ = inst.Kill() })
	require.NoError(t, inst.Start())

	dump := filepath.Join(home, "env.txt")
	require.NoError(t, inst.tmuxSession.SendKeysAndEnter("env > "+dump+"; echo dumped"))
	var env string
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(dump)
		if err != nil {
			return false
		}
		env = string(data)
		return strings.Contains(env, "AGENTDECK_")
	}, 10*time.Second, 100*time.Millisecond, "the pane shell never exported the agent-deck environment")

	assert.Contains(t, env, "AGENTDECK_INSTANCE_ID="+inst.ID+"\n")
	assert.Contains(t, env, "AGENTDECK_TOOL=shell\n")
	assert.Contains(t, env, "AGENTDECK_TITLE=parity shell\n")
	assert.Contains(t, env, "AGENTDECK_PROFILE=")
	assert.NotContains(t, env, "AGENTDECK_ACCOUNT=", "no account configured, none exported")

	want, err := inst.IdentityFilePath()
	require.NoError(t, err)
	assert.Contains(t, env, IdentityFileEnv+"="+want+"\n")
	block, err := os.ReadFile(want)
	require.NoError(t, err, "the identity file must be written where the pane runs")
	assert.Contains(t, string(block), "- session id: "+inst.ID)
	assert.Contains(t, string(block), "- tool: shell")
	assert.True(t, strings.HasPrefix(want, home), "the file lives under this host's data dir, not anywhere else")
}
