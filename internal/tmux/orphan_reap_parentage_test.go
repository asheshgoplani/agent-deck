package tmux

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The parentage check is the last gate in front of a signal in both sweeps, and
// it used to answer "orphan, sweep it" whenever it could not read the facts it
// judges on. That is fail-open in the one place both sweeps agree must fail
// closed: an unreadable parent is indistinguishable from a live sibling TUI's
// parent, and the tie was being broken toward killing.
//
// These tests pin the three unreadable cases as refusals, and — just as
// important — pin the two cases that are genuine determinations rather than
// read failures, so fail-closed does not quietly turn the sweeps inert.

func TestIsControlClientOrphanFor_FailsClosedWhenParentageIsUnreadable(t *testing.T) {
	const clientPID = 4242
	const parentPID = 999

	parentIs := func(ppid int) func(int) (int, error) {
		return func(int) (int, error) { return ppid, nil }
	}
	alive := func(int) error { return nil }
	agentDeckExe := func(int) (string, error) { return "/usr/local/bin/agent-deck", nil }
	strangerExe := func(int) (string, error) { return "/usr/bin/tmux", nil }

	cases := []struct {
		name        string
		parentPID   func(int) (int, error)
		probeAlive  func(int) error
		processExe  func(int) (string, error)
		wantVerdict parentageVerdict
	}{
		{
			name:      "parent pid unreadable while the candidate is still there",
			parentPID: func(int) (int, error) { return 0, errors.New("no /proc, and ps failed too") },
			// A host that cannot report parentage says nothing about whether a
			// live TUI owns this client.
			probeAlive: alive, processExe: agentDeckExe,
			wantVerdict: parentageUnknown,
		},
		{
			// The ordinary churn case, and NOT a refusal: candidates die during
			// the gauntlet all the time (an earlier victim's grace runs in
			// between). Reporting it as "could not establish whether a live TUI
			// owns this client" would put a Warn about a safety-relevant
			// failure on every normal sweep, and bury the ones that matter.
			name:      "the candidate itself is gone",
			parentPID: func(int) (int, error) { return 0, errors.New("no such process") },
			probeAlive: func(probed int) error {
				if probed == clientPID {
					return syscall.ESRCH
				}
				return nil
			},
			processExe:  agentDeckExe,
			wantVerdict: parentageCandidateGone,
		},
		{
			name:      "parent liveness probe refused",
			parentPID: parentIs(parentPID),
			// EPERM means the parent EXISTS and is someone else's. That is the
			// opposite of "the parent died", which is what this branch used to
			// conclude from any error at all.
			probeAlive: func(int) error { return syscall.EPERM }, processExe: agentDeckExe,
			wantVerdict: parentageUnknown,
		},
		{
			name:        "parent exe unreadable",
			parentPID:   parentIs(parentPID),
			probeAlive:  alive,
			processExe:  func(int) (string, error) { return "", errors.New("permission denied") },
			wantVerdict: parentageUnknown,
		},
		{
			// Determinations, not read failures. These must keep saying orphan,
			// or #595 cleanup goes inert and the pty table fills up again.
			name:      "reparented to init",
			parentPID: parentIs(1), probeAlive: alive, processExe: agentDeckExe,
			wantVerdict: parentageOrphaned,
		},
		{
			name:      "parent confirmed gone",
			parentPID: parentIs(parentPID),
			// ESRCH is the kernel confirming there is no such process. The
			// client is being orphaned right now.
			probeAlive: func(int) error { return syscall.ESRCH }, processExe: agentDeckExe,
			wantVerdict: parentageOrphaned,
		},
		{
			name:      "live agent-deck parent",
			parentPID: parentIs(parentPID), probeAlive: alive, processExe: agentDeckExe,
			wantVerdict: parentageOwned,
		},
		{
			name:      "live parent that is not agent-deck",
			parentPID: parentIs(parentPID), probeAlive: alive, processExe: strangerExe,
			wantVerdict: parentageOrphaned,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isControlClientOrphanFor(tc.parentPID, tc.probeAlive, tc.processExe, clientPID)
			assert.Equal(t, tc.wantVerdict, got)
			assert.Equal(t, tc.wantVerdict == parentageOrphaned, got.reapable(),
				"only a positive orphan determination may authorise a signal")
		})
	}
}

// stubControlClientOrphan swaps the parentage seam for the duration of a test.
func stubControlClientOrphan(t *testing.T, fn func(int) parentageVerdict) {
	t.Helper()
	original := controlClientOrphanOf
	t.Cleanup(func() { controlClientOrphanOf = original })
	controlClientOrphanOf = fn
}

func alwaysUnknownParentage(int) parentageVerdict { return parentageUnknown }

// TestSweepOrphanCandidates_RefusesUnknownParentage pins the orphan sweep's call
// site: an indeterminate verdict must stop the gauntlet before the signal and be
// counted, not fall through to the kill the way any non-false answer used to.
func TestSweepOrphanCandidates_RefusesUnknownParentage(t *testing.T) {
	pid := fakeTmuxCandidate(t, "ignore")

	c, _, ok := readOrphanCandidate(context.Background(), pid)
	require.True(t, ok)
	stubLiveTmuxIdentity(t, func(context.Context, int, []string) (bool, bool) { return false, true })
	stubControlClientOrphan(t, alwaysUnknownParentage)

	killed, unclassifiable, notSignalled, unknownParent, unexamined :=
		sweepOrphanCandidates(context.Background(), []orphanCandidate{c})

	assert.Equal(t, 0, killed, "a candidate whose parentage could not be read must not be killed")
	assert.Equal(t, 1, unknownParent, "the refusal must be counted, not silent")
	assert.Equal(t, 0, unclassifiable)
	assert.Equal(t, 0, notSignalled)
	assert.Equal(t, 0, unexamined)

	// The helper ignores SIGTERM, so had the sweep signalled it, the escalation
	// would have SIGKILL'd it and this would fail.
	time.Sleep(300 * time.Millisecond)
	assert.NoError(t, syscall.Kill(pid, 0), "the candidate was signalled despite an unknown verdict")
}

// TestSweepOrphanCandidates_DoesNotReportAVanishedCandidateAsARefusal separates
// the two things that used to share one answer. A candidate that exited during
// the gauntlet is ordinary churn — earlier victims' grace periods run in between
// — and counting it as "could not establish whether a live TUI owns this"
// would put a safety-shaped Warn on routine sweeps and bury the real ones.
func TestSweepOrphanCandidates_DoesNotReportAVanishedCandidateAsARefusal(t *testing.T) {
	pid := fakeTmuxCandidate(t, "ignore")

	c, _, ok := readOrphanCandidate(context.Background(), pid)
	require.True(t, ok)
	stubLiveTmuxIdentity(t, func(context.Context, int, []string) (bool, bool) { return false, true })
	stubControlClientOrphan(t, func(int) parentageVerdict { return parentageCandidateGone })

	killed, unclassifiable, notSignalled, unknownParent, unexamined :=
		sweepOrphanCandidates(context.Background(), []orphanCandidate{c})

	assert.Equal(t, 0, killed, "there is nothing left to kill")
	assert.Equal(t, 0, unknownParent,
		"a candidate that simply exited is not a refusal to establish its parentage")
	assert.Equal(t, 0, unclassifiable)
	assert.Equal(t, 0, notSignalled)
	assert.Equal(t, 0, unexamined)
}

// TestReapStaleControlClients_RefusesUnknownParentage pins the same gate in the
// other sweep. This one has no comm filter and no live-server query, so the
// parentage check is the ONLY thing standing between a `list-clients` line and a
// SIGTERM — a fail-open answer here is signalled immediately.
func TestReapStaleControlClients_RefusesUnknownParentage(t *testing.T) {
	pid := fakeTmuxCandidate(t, "ignore")
	stubControlClientOrphan(t, alwaysUnknownParentage)

	killed, _ := reapStaleControlClients(context.Background(), fmt.Sprintf("1 %d\n", pid), "(test)")

	assert.Equal(t, 0, killed)
	time.Sleep(300 * time.Millisecond)
	assert.NoError(t, syscall.Kill(pid, 0),
		"a control client whose parentage could not be read was signalled anyway")
}
