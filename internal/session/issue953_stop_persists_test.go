package session

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Issue #953: After a user manually stops a session ("agent-deck session stop"
// or pressing D in the TUI), the session is rendered as error (red ✕) instead
// of stopped (gray ■).
//
// Root cause: killInternal() executes without holding i.mu, but writes
// i.Status = StatusStopped only at the very end (after the tmux kill, MCP
// child reaping, container cleanup, and scratch-dir cleanup). A concurrent
// UpdateStatus() running in the TUI's backgroundStatusUpdate goroutine
// acquires i.mu, observes tmuxSession.Exists() == false (the tmux session
// is already dead) AND i.Status != StatusStopped (still the pre-kill value
// like StatusRunning), and races to set i.Status = StatusError under its
// lock. killInternal's later unlocked write of i.Status = StatusStopped is
// not synchronized with that read, so the data race is real and the
// observable error status can survive to the next save/render tick.
//
// The fix: take i.mu around the Status write at the end of killInternal so
// the write is properly synchronized with any concurrent UpdateStatus.
// Set the status as the very first thing under the lock — this also
// guarantees that any UpdateStatus that runs while killInternal is mid-
// cleanup sees StatusStopped (not the stale running value) and short-
// circuits at the `if i.Status != StatusStopped { i.Status = StatusError }`
// guard.

// TestIssue953_KillSetsStatusStoppedAfterKill verifies the baseline
// contract that Kill() leaves i.Status == StatusStopped. This is the
// observable invariant a user clicking D in the TUI expects.
func TestIssue953_KillSetsStatusStoppedAfterKill(t *testing.T) {
	skipIfNoTmuxBinary(t)

	inst := NewInstance("test-953-baseline", "/tmp")
	inst.Tool = "shell"
	inst.Command = "sleep 60"

	require.NoError(t, inst.Start())
	defer func() { _ = inst.Kill() }()

	// Wait past the 1.5s grace period so UpdateStatus would do real work.
	time.Sleep(2 * time.Second)
	require.NoError(t, inst.UpdateStatus())

	// Simulate the user-observed pre-kill state (the session is alive and
	// running). Without this, the baseline status would be StatusIdle and
	// the race window in killInternal would have nothing to flip to error.
	inst.SetStatusThreadSafe(StatusRunning)

	require.NoError(t, inst.Kill())

	assert.Equal(t, StatusStopped, inst.GetStatusThreadSafe(),
		"Kill() must leave Status == StatusStopped (issue #953)")
}

// TestIssue953_KillSurvivesConcurrentUpdateStatus is the RED regression
// test for the race that produces the user-visible error icon. It hammers
// UpdateStatus() in a tight loop from N background goroutines while the
// foreground goroutine calls Kill(). After Kill returns AND the hammer
// drains, the status must be StatusStopped — not StatusError.
//
// On main (without the fix) this test fails either via the -race
// detector (unsynchronized write to i.Status from killInternal) or via
// observing StatusError in the final assertion when the UpdateStatus
// race wins the last write. With the fix in place (Status write moved
// under i.mu inside killInternal), both failure modes are eliminated.
func TestIssue953_KillSurvivesConcurrentUpdateStatus(t *testing.T) {
	skipIfNoTmuxBinary(t)

	inst := NewInstance("test-953-race", "/tmp")
	inst.Tool = "shell"
	inst.Command = "sleep 60"

	require.NoError(t, inst.Start())
	defer func() { _ = inst.Kill() }()

	// Wait past the instance-level 1.5s grace period.
	time.Sleep(2 * time.Second)
	require.NoError(t, inst.UpdateStatus())

	// Pre-condition for the race: the in-memory status must be NOT-stopped
	// when killInternal begins. The TUI normally sees StatusRunning /
	// StatusWaiting / StatusIdle here; StatusRunning is the worst case
	// because it is the value most likely to be flipped to StatusError by
	// UpdateStatus when the tmux session vanishes mid-Kill.
	inst.SetStatusThreadSafe(StatusRunning)

	// Drive UpdateStatus from N goroutines in a tight loop. We want lots
	// of attempts to land between the tmux-kill and the Status-write
	// inside killInternal so the race detector and the assertion both
	// have plenty of opportunity to fire.
	const hammerGoroutines = 8
	stop := make(chan struct{})
	var hammered atomic.Int64
	var wg sync.WaitGroup
	for n := 0; n < hammerGoroutines; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				// Clear the lastErrorCheck recency optimization so the
				// hammer keeps doing real work for every iteration. Without
				// this, UpdateStatus would short-circuit at the 30s
				// errorRecheckInterval guard once the first call lands
				// after the tmux session dies.
				inst.ForceNextStatusCheck()
				_ = inst.UpdateStatus()
				hammered.Add(1)
			}
		}()
	}

	// Issue the manual stop while the hammer is running.
	require.NoError(t, inst.Kill())

	// Drain the hammer. After Kill returns, the canonical user-observable
	// status is captured here. Subsequent UpdateStatus calls SHOULD see
	// StatusStopped and short-circuit (line 3237's `if i.Status !=
	// StatusStopped` guard), so even if the hammer keeps running for a
	// few milliseconds after Kill the status must converge to
	// StatusStopped.
	close(stop)
	wg.Wait()

	t.Logf("UpdateStatus hammer iterations: %d", hammered.Load())

	assert.Equal(t, StatusStopped, inst.GetStatusThreadSafe(),
		"Status must be StatusStopped after Kill() even under concurrent UpdateStatus (issue #953)")
}

// TestIssue953_KillStatusPersistsAcrossSaveLoad is the integration arm of
// the regression: it pins the end-to-end contract a user actually
// observes between `agent-deck session stop` and the next CLI/TUI render.
//
//  1. Start a session.
//  2. Pre-set StatusRunning to simulate the user-observed pre-kill state.
//  3. Kill().
//  4. Save to SQLite.
//  5. Load from SQLite (fresh storage handle, mimicking a new process).
//  6. RefreshInstancesForCLIStatus + UpdateStatus, same as `agent-deck
//     status -v` and `agent-deck list --json` do.
//  7. Assert the rendered status is StatusStopped.
//
// This test passes on main because no race is involved in this serial
// path. It exists to lock down the persistence half of the bug so a
// future "fix" that only papers over the in-memory race (e.g. by
// reverting the StatusStopped assignment) is still caught.
func TestIssue953_KillStatusPersistsAcrossSaveLoad(t *testing.T) {
	skipIfNoTmuxBinary(t)

	inst := NewInstance("test-953-persist", "/tmp")
	inst.Tool = "shell"
	inst.Command = "sleep 60"

	require.NoError(t, inst.Start())
	defer func() { _ = inst.Kill() }()

	time.Sleep(2 * time.Second)
	require.NoError(t, inst.UpdateStatus())
	inst.SetStatusThreadSafe(StatusRunning)

	require.NoError(t, inst.Kill())
	require.Equal(t, StatusStopped, inst.GetStatusThreadSafe(),
		"baseline: Kill must set StatusStopped")

	s := newTestStorage(t)
	require.NoError(t, s.SaveWithGroups([]*Instance{inst}, nil))

	loaded, _, err := s.LoadWithGroups()
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	assert.Equal(t, StatusStopped, loaded[0].Status,
		"loaded Status must be StatusStopped (issue #953 persistence arm)")

	// Mimic `agent-deck list --json` / `agent-deck status -v`: warm the
	// CLI caches, then call UpdateStatus. The stopped session must NOT
	// flip to StatusError.
	RefreshInstancesForCLIStatus(loaded)
	require.NoError(t, loaded[0].UpdateStatus())
	assert.Equal(t, StatusStopped, loaded[0].Status,
		"UpdateStatus after CLI refresh must preserve StatusStopped (issue #953)")
}
