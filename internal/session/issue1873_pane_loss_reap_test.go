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
// It deliberately does not care HOW the restart is answered: on fixed code it
// is refused, on pre-fix code it is admitted. What is asserted is the count of
// live wrapped trees after it, which is the invariant the issue asks for.
func TestIssue1873_WrappedTreeIsReapedAfterPaneLossAndNeverDuplicatedOnRestart(t *testing.T) {
	requireEscapedWrapperSupport(t)

	w := newEscapedWrapper(t, 800*time.Millisecond)
	inst, wave1 := startEscapedInstance(t, w, "test-1873-pane-loss-reap")

	// --- Restart after pane loss: never a duplicate --------------------------
	restartErr := inst.Restart()
	// Pre-fix code admits the restart and the wrapper records its second child
	// within a second; give that duplicate time to show up so the count below
	// catches it instead of racing it.
	waitForRecordedChildren(w, 2, 3*time.Second)
	assert.Equal(t, 1, liveWrappedTrees(w),
		"a restart after pane loss must not start a second wrapped tree (restart err=%v)", restartErr)
	requireChildAlive(t, wave1, "the survivor is never signalled by a restart")

	// --- Stop after pane loss: the escaped tree is reaped --------------------
	require.NoError(t, inst.KillAndWait())
	requireChildGone(t, wave1, "a deliberate stop must reap the wrapped tree that escaped its pane")
	assert.Equal(t, 0, liveWrappedTrees(w), "nothing owned may survive a stop")

	// --- Wave 2: same shape, still exactly one tree, then none ---------------
	recorded := len(w.children())
	require.NoError(t, inst.Restart(), "restart must be admitted once nothing is owned")
	kids := w.waitForChildren(recorded+1, 15*time.Second)
	wave2 := kids[len(kids)-1]
	require.True(t, paneGoneWithin(inst, 20*time.Second), "the second wave dies the same way")
	requireChildAlive(t, wave2, "the second wrapped tree escaped its pane too")
	assert.False(t, childAlive(wave1), "the first wave's tree must still be dead")
	assert.Equal(t, 1, liveWrappedTrees(w), "exactly one wrapped tree may be alive after two waves")

	recorded = len(w.children())
	restartErr = inst.Restart()
	waitForRecordedChildren(w, recorded+1, 3*time.Second)
	assert.Equal(t, 1, liveWrappedTrees(w),
		"the second wave's restart must not duplicate either (restart err=%v)", restartErr)

	require.NoError(t, inst.KillAndWait())
	requireChildGone(t, wave2, "a stop must reap the second wave's tree too")
	assert.Equal(t, 0, liveWrappedTrees(w), "no survivor from either wave is left behind")
}

// waitForRecordedChildren waits, without failing, until the wrapper has recorded
// want children or the timeout passes. It exists for the pre-fix branch of the
// test above, where a duplicate is the expected (wrong) outcome and must be
// given time to appear.
func waitForRecordedChildren(w *escapedWrapper, want int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(w.children()) >= want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
