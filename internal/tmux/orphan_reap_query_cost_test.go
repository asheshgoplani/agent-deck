package tmux

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSweepOrphanCandidates_DoesNotQueryForACandidateItHasAlreadyRefused pins
// the ordering of the gauntlet, which is a budget property rather than a safety
// one — but this sweep's budget IS a safety property second-hand: candidates it
// never reaches are left leaking, and it only reaches them if the ones ahead do
// not waste its allowance.
//
// A comm whose role token was truncated away can never be killed. Asking the
// tmux server about it anyway costs up to 2 × tmuxLiveQueryTimeout of a shared
// orphanSweepBudget — against exactly the wedged server that produced the
// candidates — for an answer that is then thrown away.
func TestSweepOrphanCandidates_DoesNotQueryForACandidateItHasAlreadyRefused(t *testing.T) {
	queries := 0
	stubLiveTmuxIdentity(t, func(context.Context, int, []string) (bool, bool) {
		queries++
		return false, true
	})

	// Truncated comm ("tmux-3.5a:" is the shape measured in the wild), and an
	// argv that passes both scope checks, so the ONLY thing left to decide it
	// is the truncation — and that is decided before the query.
	candidate := orphanCandidate{
		pid:      1 << 30,
		comm:     "tmux-3.5a:",
		cmdline:  cadenceCmdline("truncated_probe"),
		identity: "id",
	}
	require.True(t, isTruncatedTmuxComm(candidate.comm), "the fixture must be a truncated comm")
	require.False(t, isReapableTmuxClientComm(candidate.comm), "and not classifiable as a client")

	killed, unclassifiable, notSignalled, unknownParent, unexamined :=
		sweepOrphanCandidates(context.Background(), []orphanCandidate{candidate})

	assert.Equal(t, 0, queries,
		"the sweep spent a tmux query on a candidate it had already decided it can never kill")
	assert.Equal(t, 1, unclassifiable, "the refusal must still be reported")
	assert.Equal(t, 0, killed)
	assert.Equal(t, 0, notSignalled)
	assert.Equal(t, 0, unknownParent)
	assert.Equal(t, 0, unexamined)
}
