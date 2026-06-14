package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A session that was `add`ed but never started has a tmux session object that
// does not yet exist (Start() was never called, so no tmux session was created)
// and is still in its pristine StatusIdle state. UpdateStatus must classify it
// as idle (not-yet-started), NOT error — rendering it with the ✕ error glyph is
// the TUI-verification finding.
//
// The contract that MUST stay intact: a session that WAS started (lastStartTime
// set) and then lost its tmux session is a genuine error (see
// lifecycle_regression_test.go phase5). Only the never-started case is exempt.
func TestUpdateStatus_NeverStartedSessionIsIdleNotError(t *testing.T) {
	inst := NewInstanceWithTool("never-started", t.TempDir(), "bash")
	require.Equal(t, StatusIdle, inst.GetStatusThreadSafe(), "fresh session starts idle")
	require.True(t, inst.lastStartTime.IsZero(), "a never-started session has no lastStartTime")
	require.False(t, inst.Exists(), "a never-started session has no live tmux")

	// Age past the 1.5s tmux-init grace period so we exercise the real
	// classification (not the StatusStarting shortcut).
	inst.CreatedAt = time.Now().Add(-5 * time.Second)
	inst.ForceNextStatusCheck()

	require.NoError(t, inst.UpdateStatus())

	got := inst.GetStatusThreadSafe()
	assert.NotEqual(t, StatusError, got,
		"a session that was added but never started must not show error")
	assert.Equal(t, StatusIdle, got,
		"a never-started session should remain idle")
}

// A started-then-lost session must still surface as error: gating on
// lastStartTime must not regress the externally-killed contract.
func TestUpdateStatus_StartedThenLostTmuxIsStillError(t *testing.T) {
	inst := NewInstanceWithTool("was-started", t.TempDir(), "bash")
	// Simulate a session that WAS started but whose tmux session is now gone:
	// non-zero lastStartTime + a non-existent tmux session, aged past grace.
	inst.lastStartTime = time.Now().Add(-5 * time.Second)
	inst.CreatedAt = inst.lastStartTime
	inst.Status = StatusRunning // it had been running before tmux died
	inst.ForceNextStatusCheck()

	require.NoError(t, inst.UpdateStatus())

	assert.Equal(t, StatusError, inst.GetStatusThreadSafe(),
		"a session that was started and then lost its tmux must show error")
}
