package tmux

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestReapableVerbsMatchTheBoundedCommandLists pins the claim
// reapableOneShotVerbs makes about itself: "the set mirrors the poll/mutation
// lists that TestPollCommandsAreBounded enforces deadlines for — same commands,
// same reason: they run on a cadence, so they are the ones that leak."
//
// It does mirror them today, and nothing made it stay that way. The lint's own
// comments record its lists being widened twice ("the status-bar and key-binding
// commands join them ... one round later"), and each widening teaches the lint
// about a new cadence command while leaving the reaper unable to reap that
// command's orphans — which is the sweep quietly going inert for one verb at a
// time, the exact failure this whole area exists to prevent, and invisible
// because a narrower sweep never fails a test.
//
// Deliberately an equality assertion rather than a subset one. A verb the
// reaper may kill but the lint does not bound is just as wrong in the other
// direction: it means agent-deck spawns that command unbounded, so the sweep is
// authorised to kill a process class that this package never promised to stop
// leaking.
func TestReapableVerbsMatchTheBoundedCommandLists(t *testing.T) {
	keys := func(m map[string]struct{}) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}

	bounded := map[string]struct{}{}
	for verb := range pollSubcommands {
		bounded[verb] = struct{}{}
	}
	for verb := range mutationSubcommands {
		bounded[verb] = struct{}{}
	}

	assert.Equal(t, keys(bounded), keys(reapableOneShotVerbs),
		"reapableOneShotVerbs must equal pollSubcommands ∪ mutationSubcommands. "+
			"A verb bounded by the lint but missing here leaks orphans the sweep cannot reap; "+
			"a verb here but not bounded there is one this package never promised to stop leaking. "+
			"If you are adding a cadence command, add it to both.")
}
