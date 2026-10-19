package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// #2805: a second Start on the same Instance rewrites tmux.Session.Name (the
// old tmux session still exists, so Start picks a fresh name) while the first
// spawn's fast-death watcher is still polling Exists/IsPaneDead/CapturePane on
// that same *tmux.Session. Under -race that was reported as a write in
// tmux.(*Session).Start racing the read of s.Name in CapturePane. Start now
// supersedes and joins the previous watcher before tmux is asked to start
// again, so the watcher's reads happen before the rename.
func TestIssue2805_RestartDoesNotRaceFastDeathWatcher(t *testing.T) {
	skipIfNoTmuxBinary(t)

	inst := NewInstance("test-2805-start-race", t.TempDir())
	inst.Tool = "customlive2805"
	inst.Command = "sleep 60"
	t.Cleanup(func() {
		_ = inst.Kill()
		inst.waitForFastDeathWatchers()
		clearSpawnFailureRecord(inst.ID)
	})

	require.NoError(t, inst.Start())
	// Let the watcher take a few ticks, so it has read Name through
	// CapturePane (both the subprocess and the cache-hit path).
	time.Sleep(3 * spawnFastDeathTick)
	firstName := inst.tmuxSession.Name
	t.Cleanup(func() { _ = tmux.ReconnectSession(firstName, firstName, "", "").Kill() })
	// Only one restart: a further Start would be refused by the ownership
	// guard, since the first pane is still alive outside the session.
	require.NoError(t, inst.Start())
	require.NotEqual(t, firstName, inst.tmuxSession.Name,
		"Start on a still-existing tmux session must pick a fresh name, or this test exercises nothing")
}
