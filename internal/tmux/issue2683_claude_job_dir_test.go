package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Issue #2683: agent-deck started from inside a Claude Code background job
// carries CLAUDE_JOB_DIR. If the tmux server it starts inherits it, every
// pane does too, and the hook handler then drops every hook on that server
// as "from a background worker". The tmux spawn env must strip it.
func TestStart_PaneDoesNotInheritClaudeJobDir(t *testing.T) {
	skipIfNoTmuxBinary(t)

	socket := "ad2683-jobdir"
	kill := func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() }
	kill()
	t.Cleanup(kill)

	t.Setenv("CLAUDE_JOB_DIR", filepath.Join(t.TempDir(), "jobs", "abc"))

	marker := filepath.Join(t.TempDir(), "env.out")
	s := &Session{
		Name:                       "agentdeck_2683_jobdir",
		DisplayName:                "jobdir",
		SocketName:                 socket,
		WorkDir:                    t.TempDir(),
		RunCommandAsInitialProcess: true,
	}
	err := s.Start("/bin/sh -c 'echo \"job=${CLAUDE_JOB_DIR-unset}\" > " + marker + "; sleep 5'")
	if err == nil {
		t.Cleanup(func() { _ = s.Kill() })
	}
	require.NoError(t, err)

	deadline := time.Now().Add(3 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		if data, readErr := os.ReadFile(marker); readErr == nil && strings.TrimSpace(string(data)) != "" {
			got = strings.TrimSpace(string(data))
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.Equal(t, "job=unset", got, "the pane inherited CLAUDE_JOB_DIR from agent-deck's env")

	// The server's global environment must not carry it either, or every
	// later pane (new-window, respawn) would inherit it.
	out, _ := exec.Command("tmux", "-L", socket, "show-environment", "-g", "CLAUDE_JOB_DIR").CombinedOutput()
	require.False(t, strings.HasPrefix(strings.TrimSpace(string(out)), "CLAUDE_JOB_DIR="),
		"tmux server global env carries CLAUDE_JOB_DIR: %s", out)
}
