package session

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/stretchr/testify/require"
)

func TestStoppedSessionSurvivesStartupGraceRefresh(t *testing.T) {
	skipIfNoTmuxBinary(t)

	inst := NewInstanceWithTool("stop-grace", t.TempDir(), "shell")
	inst.Command = "sleep 60"
	require.NoError(t, inst.Start())
	t.Cleanup(func() { _ = inst.Kill() })
	require.NoError(t, inst.Kill())
	stoppedTmux := func() bool {
		sess := inst.GetTmuxSession()
		return tmux.HasSessionOnSocket(sess.SocketName, sess.Name)
	}
	require.False(t, stoppedTmux(), "stop must remove the tmux session")

	start := time.Now()
	for _, at := range []time.Duration{0, 500 * time.Millisecond, 5 * time.Second} {
		if wait := at - time.Since(start); wait > 0 {
			time.Sleep(wait)
		}
		inst.mu.Lock()
		// Start teardown can take longer than the grace window on a busy
		// runner. Pin the first sample inside it, then exercise real time.
		if at == 0 {
			inst.lastStartTime = time.Now()
		}
		// ForceNextStatusCheck only clears idle throttling, not this one.
		inst.lastErrorCheck = time.Time{}
		inst.mu.Unlock()
		inst.ForceNextStatusCheck()
		require.NoError(t, inst.UpdateStatus())
		require.Equalf(t, StatusStopped, inst.GetStatusThreadSafe(), "status at +%s", at)
		require.Falsef(t, stoppedTmux(), "tmux session at +%s", at)
	}
}
