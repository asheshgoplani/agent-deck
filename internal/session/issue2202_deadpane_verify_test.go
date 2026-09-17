package session

import (
	"errors"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Issue #2202: session start could report success after tmux creation even
// when the initial pane command had already exited. #2099/#2265 fixed the
// case where the whole tmux session vanishes at once (default tmux
// behaviour), but sandbox sessions (and anything else with remain-on-exit
// set) keep the tmux session alive with a dead pane when the initial
// command exits immediately — VerifySpawned only checked ProbeExists, so it
// reported success for a session whose command had already exited with any
// code, including 0.

// TestIssue2202_VerifySpawnedRemainOnExitDiesFast: a remain-on-exit session
// whose initial command exits immediately must not verify as started, even
// though the tmux session itself is still alive (dead pane, not gone).
func TestIssue2202_VerifySpawnedRemainOnExitDiesFast(t *testing.T) {
	skipIfNoTmuxBinary(t)

	inst := NewInstance("test-2202-remainonexit", "/tmp")
	inst.Tool = "customfail2202"
	inst.tmuxSession = tmux.NewSession(inst.Title, "/tmp")
	inst.tmuxSession.OptionOverrides = map[string]string{"remain-on-exit": "on"}
	inst.tmuxSession.RunCommandAsInitialProcess = true
	t.Cleanup(func() {
		_ = inst.tmuxSession.Kill()
		clearSpawnFailureRecord(inst.ID)
	})

	// Start a genuinely long-lived initial process first, so Start() has
	// definitely finished applying the remain-on-exit override (proven by
	// TestIssue2202_VerifySpawnedRemainOnExitHealthy below) before anything
	// dies — racing the override against a command written to exit
	// immediately would leave this test at the mercy of Start()'s own
	// internal timing (bind-key, status bar, pane-ready wait) instead of
	// exercising the behavior under test. Interrupting the running process
	// afterward reproduces "the initial command exited" deterministically:
	// remain-on-exit is already active, so tmux keeps the session with a
	// dead pane instead of tearing it down — exactly the #2202 case.
	const cmd = "sleep 60"
	require.NoError(t, inst.tmuxSession.Start(cmd))
	inst.startFastDeathWatcher(cmd, inst.spawnGen.Load(), nil, inst.tmuxSession, inst.ID, inst.Tool, sessionLog)
	require.NoError(t, inst.tmuxSession.SendCtrlC())

	err := inst.VerifySpawned(5 * time.Second)
	require.Error(t, err, "a remain-on-exit session whose pane died immediately must not verify as started")

	var spawnErr *SpawnFailedError
	require.True(t, errors.As(err, &spawnErr), "error must be a *SpawnFailedError, got %T", err)
	require.NotNil(t, spawnErr.Record, "the fast-death watcher's record must be read back")
	assert.Equal(t, "spawn_died_fast", spawnErr.Record.Reason)

	// The tmux session must still exist (remain-on-exit kept it) — this is
	// exactly the case ProbeExists()-only verification missed.
	exists, probeErr := inst.tmuxSession.ProbeExists()
	require.NoError(t, probeErr)
	assert.True(t, exists, "remain-on-exit must keep the tmux session alive with a dead pane")
}

// TestIssue2202_VerifySpawnedRemainOnExitHealthy: a remain-on-exit session
// whose command is still running must verify cleanly and quickly, exactly
// like the non-remain-on-exit case.
func TestIssue2202_VerifySpawnedRemainOnExitHealthy(t *testing.T) {
	skipIfNoTmuxBinary(t)

	inst := NewInstance("test-2202-remainonexit-healthy", "/tmp")
	inst.Tool = "customlive2202"
	inst.tmuxSession = tmux.NewSession(inst.Title, "/tmp")
	inst.tmuxSession.OptionOverrides = map[string]string{"remain-on-exit": "on"}
	inst.tmuxSession.RunCommandAsInitialProcess = true
	t.Cleanup(func() {
		_ = inst.tmuxSession.Kill()
		clearSpawnFailureRecord(inst.ID)
	})

	require.NoError(t, inst.tmuxSession.Start("sleep 60"))
	inst.startFastDeathWatcher("sleep 60", inst.spawnGen.Load(), nil, inst.tmuxSession, inst.ID, inst.Tool, sessionLog)

	started := time.Now()
	require.NoError(t, inst.VerifySpawned(3*time.Second))
	assert.Less(t, time.Since(started), 2*time.Second, "a live remain-on-exit session must verify without waiting out the window")
}
