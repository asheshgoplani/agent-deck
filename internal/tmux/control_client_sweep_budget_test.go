package tmux

import (
	"context"
	"fmt"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reapStaleControlClients had no deadline of its own. staleControlSweepTimeout
// bounded only the `list-clients` query feeding it, so the sweep that follows —
// one identity read per pid, then a SIGTERM and up to controlClientKillGrace
// per victim — ran unbounded, synchronously, on the boot path (main.go's
// SweepStaleControlClients, immediately before the TUI starts). Over the
// backlog this function documents (176 orphaned clients), that is a startup
// stall measured in minutes.
//
// Commit 3a5af543 makes exactly this argument to get the ORPHAN sweep off the
// Connect path and did not apply it here, which the maintainer's review called
// out as a non-blocking finding.

// TestReapStaleControlClients_StopsAndReportsWhenTheBudgetIsSpent is the
// counterpart of TestSweepOrphanCandidates_StopsAndReportsWhenTheBudgetIsSpent:
// a spent budget must stop the sweep before it signals anything, and the
// candidates it never reached must be reported rather than silently dropped.
func TestReapStaleControlClients_StopsAndReportsWhenTheBudgetIsSpent(t *testing.T) {
	first := fakeTmuxCandidate(t, "ignore")
	second := fakeTmuxCandidate(t, "ignore")

	spent, cancel := context.WithCancel(context.Background())
	cancel()

	killed, unexamined := reapStaleControlClients(spent,
		fmt.Sprintf("1 %d\n1 %d\n", first, second), "(test)")

	assert.Equal(t, 0, killed, "a spent budget must stop the sweep before it signals anything")
	assert.Equal(t, 2, unexamined, "every candidate the sweep never reached must be reported")

	// Both helpers ignore SIGTERM, so had either been signalled the escalation
	// would have SIGKILL'd it.
	time.Sleep(300 * time.Millisecond)
	assert.NoError(t, syscall.Kill(first, 0), "the first candidate was signalled despite a spent budget")
	assert.NoError(t, syscall.Kill(second, 0), "the second candidate was signalled despite a spent budget")
}

// TestSweepStaleControlClients_BoundsTheWholeSweepNotJustTheQuery pins the
// wiring at the boot-path entry point. Bounding `list-clients` and then handing
// the sweep a context.Background() is the shape this fix exists to remove, and
// it is invisible from inside reapStaleControlClients — only the caller can be
// wrong about it.
func TestSweepStaleControlClients_BoundsTheWholeSweepNotJustTheQuery(t *testing.T) {
	socket := orphanLiveTestSocket(t)
	const session = "agentdeck_sweep_budget"
	startOrphanLiveSession(t, socket, session)
	reparentedControlClient(t, socket, session, "attach-session", "-t", session)

	// Record the context the sweep hands its identity reads, and refuse the
	// read so nothing is signalled: this test is about the deadline, not kills.
	var gotDeadline bool
	var sawRead bool
	original := processIdentityOf
	t.Cleanup(func() { processIdentityOf = original })
	processIdentityOf = func(ctx context.Context, _ int) (string, error) {
		sawRead = true
		_, gotDeadline = ctx.Deadline()
		return "", fmt.Errorf("refused by the test")
	}

	SweepStaleControlClients(socket)

	require.True(t, sawRead,
		"the sweep never reached an identity read — the fixture is not exercising the path under test")
	assert.True(t, gotDeadline,
		"the sweep's identity reads ran under a context with no deadline: the whole sweep, "+
			"not just its list-clients query, must be bounded on the boot path")
}
