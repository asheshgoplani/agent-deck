package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIssue1873_WrappedTreeIsReapedAfterPaneLossAndNeverDuplicatedOnRestart is
// the end-to-end acceptance test for #1873, written against seams that exist
// on pre-fix code so it fails there for the reasons the issue reports rather
// than failing to compile:
//
//   - a pane that dies during startup leaves the wrapped tree alive;
//   - a later restart must not put a second tree next to it;
//   - a deliberate stop must reap the tree even though its pane is long gone;
//   - a second fast-death/restart wave must leave exactly one tree, then none.
//
// It is deterministic, not timed. The wrapper is held alive until the receipt
// on disk names the escaped child, and the test FAILS if that handshake does
// not complete, so attribution can never lose a scheduling race to the pane's
// death. (Set AGENTDECK_1873_BASELINE=1 to run it against pre-fix code, where
// no receipt exists: the handshake is then allowed to expire and the test fails
// on the issue's own assertions instead.) The restart is judged by its outcome,
// not by a sleep: a restart that returns nil must have produced a recorded
// second child (the fixture fails loudly if it did not), and the live-tree
// count is asserted after that.
func TestIssue1873_WrappedTreeIsReapedAfterPaneLossAndNeverDuplicatedOnRestart(t *testing.T) {
	requireEscapedWrapperSupport(t)

	w := newSynchronisedEscapedWrapper(t)
	inst, wave1 := startEscapedInstance(t, w, "test-1873-pane-loss-reap")

	// --- Restart after pane loss: never a duplicate --------------------------
	restartIsRefusedOrProvesADuplicate(t, inst, w)
	assert.Equal(t, 1, liveWrappedTrees(w),
		"a restart after pane loss must not start a second wrapped tree")
	requireChildAlive(t, wave1, "the survivor is never signalled by a restart")

	// --- Stop after pane loss: the escaped tree is reaped --------------------
	stopEscapedInstance(t, inst, true)
	requireChildGone(t, wave1, "a deliberate stop must reap the wrapped tree that escaped its pane")
	assert.Equal(t, 0, liveWrappedTrees(w), "nothing owned may survive a stop")

	// --- Wave 2: same shape, still exactly one tree, then none ---------------
	recorded := len(w.children())
	require.NoError(t, inst.Restart(), "restart must be admitted once nothing is owned")
	kids := w.waitForChildren(recorded+1, 15*time.Second)
	wave2 := kids[len(kids)-1]
	requireReceiptHandshake(t, inst.ID, wave2)
	w.release()
	require.True(t, paneGoneWithin(inst, 20*time.Second), "the second wave dies the same way")
	requireChildAlive(t, wave2, "the second wrapped tree escaped its pane too")
	assert.False(t, childAlive(wave1), "the first wave's tree must still be dead")
	assert.Equal(t, 1, liveWrappedTrees(w), "exactly one wrapped tree may be alive after two waves")

	restartIsRefusedOrProvesADuplicate(t, inst, w)
	assert.Equal(t, 1, liveWrappedTrees(w), "the second wave's restart must not duplicate either")

	stopEscapedInstance(t, inst, true)
	requireChildGone(t, wave2, "a stop must reap the second wave's tree too")
	assert.Equal(t, 0, liveWrappedTrees(w), "no survivor from either wave is left behind")
}

// restartIsRefusedOrProvesADuplicate calls Restart and pins down the outcome
// either way. A refusal is the fixed behaviour and must leave no replacement
// pane. An admitted restart (pre-fix) must show its second child in the record
// before the caller counts live trees — waiting on the record rather than on a
// clock is what keeps the count honest on both sides.
func restartIsRefusedOrProvesADuplicate(t *testing.T, inst *Instance, w *escapedWrapper) {
	t.Helper()
	recorded := len(w.children())
	err := inst.Restart()
	if err != nil {
		assert.False(t, paneAliveNow(inst.GetTmuxSession()),
			"a refused restart must not have started a replacement pane: %v", err)
		assert.Len(t, w.children(), recorded, "a refused restart must not have recorded a new child")
		return
	}
	// Admitted: the duplicate is a certainty, not a possibility, and the
	// fixture fails the test loudly if it never shows up.
	w.waitForChildren(recorded+1, 15*time.Second)
}
