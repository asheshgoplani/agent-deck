package tmux

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two guards in sweepOrphanCandidates narrow what it may kill, and a mutation
// run found both of them unpinned: delete either and the package stayed green.
// The SessionPrefix one is the one that matters — reapOrphanedPollClients'
// own doc calls it the boundary that keeps the sweep off a tmux client that is
// not agent-deck's — and "green with the boundary deleted" is exactly the shape
// of the two incidents behind this file.
//
// Both tests below use a REAL live process that fails the guard under test and
// passes everything else, so the guard is the only thing between it and a
// SIGTERM/SIGKILL. The helper ignores SIGTERM, which makes its survival
// unambiguous: had the sweep signalled it, the escalation would have killed it.

// sweepWithNothingLive runs the gauntlet with the live-server query stubbed to
// its most kill-permissive answer ("classifiable, and not live"), so nothing
// downstream of the scope guards can be the reason a candidate survives.
func sweepWithNothingLive(t *testing.T, c orphanCandidate) (killed, unclassifiable, notSignalled, unknownParent, unexamined int) {
	t.Helper()
	stubLiveTmuxIdentity(t, func(context.Context, int, []string) (bool, bool) { return false, true })
	return sweepOrphanCandidates(context.Background(), []orphanCandidate{c})
}

// assertSurvivedTheSweep waits out the SIGTERM grace and the SIGKILL behind it,
// then checks the pid is still there. These helpers are reparented to init,
// which reaps them promptly, so the pid genuinely disappears when killed rather
// than lingering as a zombie that would answer signal 0 either way.
func assertSurvivedTheSweep(t *testing.T, pid int, why string) {
	t.Helper()
	time.Sleep(controlClientKillGrace + 300*time.Millisecond)
	assert.NoError(t, syscall.Kill(pid, 0), why)
}

// TestSweepOrphanCandidates_LeavesATmuxClientOutsideAgentDeckAlone pins the
// SessionPrefix scope check. The candidate is a genuine orphaned tmux client
// running a cadence verb — everything the sweep looks for — except that the
// session it targets is not agent-deck's. Nothing else in the gauntlet
// distinguishes it, so without this check the sweep reaps other software's
// tmux clients.
func TestSweepOrphanCandidates_LeavesATmuxClientOutsideAgentDeckAlone(t *testing.T) {
	pid := fakeTmuxCandidateArgv(t, "ignore",
		[]string{"-L", "orphan-scope-test", "list-clients", "-t", "someoneelses_session"})

	c, _, ok := readOrphanCandidate(context.Background(), pid)
	require.True(t, ok, "the candidate must reach the gauntlet, or this test proves nothing")
	require.NotContains(t, c.cmdline, SessionPrefix, "the candidate must fail the guard under test")

	killed, unclassifiable, notSignalled, unknownParent, unexamined := sweepWithNothingLive(t, c)

	assert.Equal(t, 0, killed, "a tmux client targeting a session that is not agent-deck's must not be killed")
	assert.Equal(t, 0, unclassifiable)
	assert.Equal(t, 0, notSignalled)
	assert.Equal(t, 0, unknownParent)
	assert.Equal(t, 0, unexamined)
	assertSurvivedTheSweep(t, pid, "a tmux client outside agent-deck's own sessions was signalled")
}

// TestSweepOrphanCandidates_LeavesANonCadenceVerbAlone pins the
// isKnownCadenceArgv call site. The verb allowlist is scope, not safety — its
// own doc says so — but scope is what keeps the sweep to the short-lived
// commands whose loss costs nothing, and a candidate running something else is
// not one the sweep may guess about.
func TestSweepOrphanCandidates_LeavesANonCadenceVerbAlone(t *testing.T) {
	pid := fakeTmuxCandidateArgv(t, "ignore",
		[]string{"-L", "orphan-scope-test", "rename-window", "-t", SessionPrefix + "scope_probe", "newname"})

	c, _, ok := readOrphanCandidate(context.Background(), pid)
	require.True(t, ok)
	require.Contains(t, c.cmdline, SessionPrefix,
		"the candidate must pass the OTHER scope guard, so only the verb check is under test")
	require.False(t, isKnownCadenceArgv(c.cmdline), "the candidate must fail the guard under test")

	killed, unclassifiable, notSignalled, unknownParent, unexamined := sweepWithNothingLive(t, c)

	assert.Equal(t, 0, killed, "a client running a verb outside the cadence allowlist must not be killed")
	assert.Equal(t, 0, unclassifiable)
	assert.Equal(t, 0, notSignalled)
	assert.Equal(t, 0, unknownParent)
	assert.Equal(t, 0, unexamined)
	assertSurvivedTheSweep(t, pid, "a client running a non-cadence verb was signalled")
}
