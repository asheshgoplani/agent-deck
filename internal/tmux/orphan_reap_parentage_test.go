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
		name       string
		parentPID  func(int) (int, error)
		probeAlive func(int) error
		processExe func(int) (string, error)
		wantOrphan bool
		wantKnown  bool
	}{
		{
			name:      "parent pid unreadable",
			parentPID: func(int) (int, error) { return 0, errors.New("no /proc, and ps failed too") },
			// A host that cannot report parentage says nothing about whether a
			// live TUI owns this client.
			probeAlive: alive, processExe: agentDeckExe,
			wantOrphan: false, wantKnown: false,
		},
		{
			name:      "parent liveness probe refused",
			parentPID: parentIs(parentPID),
			// EPERM means the parent EXISTS and is someone else's. That is the
			// opposite of "the parent died", which is what this branch used to
			// conclude from any error at all.
			probeAlive: func(int) error { return syscall.EPERM }, processExe: agentDeckExe,
			wantOrphan: false, wantKnown: false,
		},
		{
			name:       "parent exe unreadable",
			parentPID:  parentIs(parentPID),
			probeAlive: alive,
			processExe: func(int) (string, error) { return "", errors.New("permission denied") },
			wantOrphan: false, wantKnown: false,
		},
		{
			// Determinations, not read failures. These must keep saying orphan,
			// or #595 cleanup goes inert and the pty table fills up again.
			name:      "reparented to init",
			parentPID: parentIs(1), probeAlive: alive, processExe: agentDeckExe,
			wantOrphan: true, wantKnown: true,
		},
		{
			name:      "parent confirmed gone",
			parentPID: parentIs(parentPID),
			// ESRCH is the kernel confirming there is no such process. The
			// client is being orphaned right now.
			probeAlive: func(int) error { return syscall.ESRCH }, processExe: agentDeckExe,
			wantOrphan: true, wantKnown: true,
		},
		{
			name:      "live agent-deck parent",
			parentPID: parentIs(parentPID), probeAlive: alive, processExe: agentDeckExe,
			wantOrphan: false, wantKnown: true,
		},
		{
			name:      "live parent that is not agent-deck",
			parentPID: parentIs(parentPID), probeAlive: alive, processExe: strangerExe,
			wantOrphan: true, wantKnown: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orphan, known := isControlClientOrphanFor(tc.parentPID, tc.probeAlive, tc.processExe, clientPID)
			assert.Equal(t, tc.wantKnown, known, "known")
			assert.Equal(t, tc.wantOrphan, orphan, "orphan")
			if !known {
				assert.False(t, orphan,
					"an unknown verdict must never also read as orphan: every caller "+
						"that ignores `known` would then signal on a guess")
			}
		})
	}
}

// stubControlClientOrphan swaps the parentage seam for the duration of a test.
func stubControlClientOrphan(t *testing.T, fn func(int) (bool, bool)) {
	t.Helper()
	original := controlClientOrphanOf
	t.Cleanup(func() { controlClientOrphanOf = original })
	controlClientOrphanOf = fn
}

// unknownParentage is the verdict the two call-site tests below stub in:
// known=false, and orphan=true underneath it.
//
// isControlClientOrphanFor never produces that pair — the unit test above pins
// that it cannot — and that is exactly why it is the right stub here. A caller
// that reads `orphan` and ignores `known` sends the signal; a caller that checks
// `known` first refuses. Stubbing known=false with orphan=false instead would
// pass either way, since both gates end in "leave it alone", and would pin
// nothing.
func unknownParentage(int) (bool, bool) { return true, false }

// TestSweepOrphanCandidates_RefusesUnknownParentage pins the orphan sweep's call
// site: an indeterminate verdict must stop the gauntlet before the signal and be
// counted, not fall through to the kill the way any non-false answer used to.
func TestSweepOrphanCandidates_RefusesUnknownParentage(t *testing.T) {
	pid := fakeTmuxCandidate(t, "ignore")

	c, _, ok := readOrphanCandidate(context.Background(), pid)
	require.True(t, ok)
	stubLiveTmuxIdentity(t, func(context.Context, int, []string) (bool, bool) { return false, true })
	stubControlClientOrphan(t, unknownParentage)

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

// TestReapStaleControlClients_RefusesUnknownParentage pins the same gate in the
// other sweep. This one has no comm filter and no live-server query, so the
// parentage check is the ONLY thing standing between a `list-clients` line and a
// SIGTERM — a fail-open answer here is signalled immediately.
func TestReapStaleControlClients_RefusesUnknownParentage(t *testing.T) {
	pid := fakeTmuxCandidate(t, "ignore")
	stubControlClientOrphan(t, unknownParentage)

	killed := reapStaleControlClients(fmt.Sprintf("1 %d\n", pid), "(test)")

	assert.Equal(t, 0, killed)
	time.Sleep(300 * time.Millisecond)
	assert.NoError(t, syscall.Kill(pid, 0),
		"a control client whose parentage could not be read was signalled anyway")
}
