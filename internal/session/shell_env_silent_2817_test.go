package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #2817 (reported by @tomasaschan): every new shell session visibly typed a
// long ` export AGENTDECK_INSTANCE_ID=... AGENTDECK_IDENTITY_FILE=...` line
// into the pane (ensureInteractiveShellEnv). The shell must instead be born
// with the same variables, on start and on restart, with nothing typed, and
// a title full of shell metacharacters must arrive verbatim.
func TestShellSession_EnvAtLaunch_NothingTyped_Issue2817(t *testing.T) {
	skipIfNoTmuxBinary(t)
	if out, err := exec.Command("tmux", "-V").Output(); err == nil && tmuxOlderThan32(string(out)) {
		t.Skip("tmux < 3.2 has no new-session -e; the typed export is the fallback there")
	}
	home := withTempAgentDeckHome(t, "")
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("AGENTDECK_ACCOUNT", "")
	ClearUserConfigCache()

	// tmux reads an argv token ending in ';' as a command separator, so a
	// title ending in ';' (or in `\;`) must neither break the spawn nor
	// arrive changed (review of #2817).
	for i, title := range []string{`it's a "$(x)" test`, `build; deploy;`, `odd\;`} {
		inst := NewInstanceWithTool(title, t.TempDir(), "shell")
		t.Cleanup(func() { _ = inst.Kill() })
		checkShellEnvAtLaunch(t, home, inst, title, strconv.Itoa(i))
	}
}

func checkShellEnvAtLaunch(t *testing.T, home string, inst *Instance, title, tag string) {
	t.Helper()

	check := func(phase string) {
		t.Helper()
		dump := filepath.Join(home, tag+"-"+phase+"-env.txt")
		require.NoError(t, inst.tmuxSession.SendKeysAndEnter("env > "+dump))
		var env string
		require.Eventually(t, func() bool {
			data, err := os.ReadFile(dump)
			if err != nil {
				return false
			}
			env = string(data)
			return strings.Contains(env, "AGENTDECK_INSTANCE_ID=")
		}, 10*time.Second, 100*time.Millisecond, "%s: the pane shell never got the agent-deck environment", phase)

		assert.Contains(t, env, "AGENTDECK_INSTANCE_ID="+inst.ID+"\n", phase)
		assert.Contains(t, env, "AGENTDECK_TOOL=shell\n", phase)
		assert.Contains(t, env, "AGENTDECK_TITLE="+title+"\n", phase)
		assert.Contains(t, env, "AGENTDECK_PROFILE="+sessionProfileEnvValue()+"\n", phase)
		want, err := inst.IdentityFilePath()
		require.NoError(t, err)
		assert.Contains(t, env, IdentityFileEnv+"="+want+"\n", phase)

		pane, err := inst.tmuxSession.CaptureFullHistory()
		require.NoError(t, err)
		assert.NotContains(t, pane, "export AGENTDECK_", "%s: agent-deck typed an export line into the pane:\n%s", phase, pane)
		assert.NotContains(t, pane, "AGENTDECK_INSTANCE_ID", "%s: agent-deck typed into the pane:\n%s", phase, pane)
	}

	require.NoError(t, inst.Start())
	check("start")

	require.NoError(t, inst.Restart())
	check("restart")
}

// tmuxOlderThan32 reports whether `tmux -V` output names a release before
// 3.2 (no new-session -e). Unparseable output ("master") counts as new.
func tmuxOlderThan32(v string) bool {
	m := regexp.MustCompile(`(\d+)\.(\d+)`).FindStringSubmatch(v)
	if m == nil {
		return false
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	return major < 3 || (major == 3 && minor < 2)
}
