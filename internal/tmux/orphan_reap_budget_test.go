package tmux

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The orphan sweep asks a tmux server about every candidate it cannot rule out
// cheaply, and those queries are slowest on exactly the wedged host that
// produced the orphans. Two properties keep that from becoming the problem it
// is meant to solve: the whole sweep runs under ONE deadline, and it does not
// run on the goroutine that opens sessions.

// TestSweepOrphanCandidates_StopsAndReportsWhenTheBudgetIsSpent pins the first.
// A sweep out of budget stops where it is and says how much it never looked at
// — it does not keep querying, and it does not return counts that read like a
// clean run. Silent truncation here would recreate the original failure of this
// code: a sweep that did nothing looked exactly like one that found nothing.
func TestSweepOrphanCandidates_StopsAndReportsWhenTheBudgetIsSpent(t *testing.T) {
	spent, cancel := context.WithCancel(context.Background())
	cancel()

	queries := 0
	stubLiveTmuxIdentity(t, func(context.Context, int, []string) (bool, bool) {
		queries++
		return false, true
	})

	candidates := []orphanCandidate{
		{pid: 1 << 30, comm: "tmux: client", cmdline: cadenceCmdline("a"), identity: "id-a"},
		{pid: 1<<30 + 1, comm: "tmux: client", cmdline: cadenceCmdline("b"), identity: "id-b"},
	}

	killed, unclassifiable, notSignalled, unknownParent, unexamined := sweepOrphanCandidates(spent, candidates)

	assert.Equal(t, 0, killed)
	assert.Equal(t, 0, unclassifiable)
	assert.Equal(t, 0, notSignalled)
	assert.Equal(t, 0, unknownParent)
	assert.Equal(t, len(candidates), unexamined, "every unjudged candidate must be reported")
	assert.Equal(t, 0, queries, "a spent budget must stop the queries, not just the kills")
}

// TestSweepOrphanCandidates_BudgetIsAggregateNotPerCandidate pins that the
// deadline covers the sweep as a whole. Before this, each candidate carried its
// own two-second allowance, so total cost scaled with the number of candidates
// — twenty orphans meant a sweep nobody had bounded.
//
// Each stubbed query burns a slice of a deliberately small budget, so the run
// must stop partway through a candidate list that a per-candidate allowance
// would have walked to the end.
func TestSweepOrphanCandidates_BudgetIsAggregateNotPerCandidate(t *testing.T) {
	budget, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	t.Cleanup(cancel)

	stubLiveTmuxIdentity(t, func(ctx context.Context, _ int, _ []string) (bool, bool) {
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
		}
		return true, true // live: preserved, so nothing here is ever signalled
	})

	candidates := make([]orphanCandidate, 20)
	for i := range candidates {
		candidates[i] = orphanCandidate{
			pid:      1<<30 + i,
			comm:     "tmux: client",
			cmdline:  cadenceCmdline("s"),
			identity: "id",
		}
	}

	killed, _, _, _, unexamined := sweepOrphanCandidates(budget, candidates)

	assert.Equal(t, 0, killed)
	assert.Positive(t, unexamined,
		"the sweep must stop at its own deadline; a per-candidate allowance would have judged all %d",
		len(candidates))
}

// TestIsLiveTmuxClientOrServer_RefusesOnceTheBudgetIsSpent pins the nesting
// underneath: the per-probe cap is derived FROM the budget rather than being a
// separate allowance, so a probe started after the budget is gone cannot run at
// all — and a query that produced no answer is unclassifiable, which the sweep
// treats as "refuse to kill". No tmux server is involved: there is nothing left
// to ask with.
func TestIsLiveTmuxClientOrServer_RefusesOnceTheBudgetIsSpent(t *testing.T) {
	spent, cancel := context.WithCancel(context.Background())
	cancel()

	live, ok := isLiveTmuxClientOrServer(spent, 1<<30, []string{"tmux", "-L", "no-such-socket", "list-clients"})

	assert.False(t, ok, "a query with no budget left cannot classify anything")
	assert.False(t, live)
}

// TestStartOrphanReapOnce_RunsInTheBackgroundExactlyOnce pins the second
// property. The sweep sits on Connect, which is the fleet-restore path, and it
// now costs tmux subprocesses per candidate — so it must not be something a
// session open waits for, and twenty sessions opening at once must still
// produce exactly one sweep.
func TestStartOrphanReapOnce_RunsInTheBackgroundExactlyOnce(t *testing.T) {
	originalFn := orphanReapFn
	originalStarted := orphanReapStarted.Load()
	t.Cleanup(func() {
		orphanReapFn = originalFn
		// Restore whatever the suite had — TestMain marks the sweep started so
		// no Connect launches a real one mid-suite.
		orphanReapStarted.Store(originalStarted)
	})
	orphanReapStarted.Store(false)

	release := make(chan struct{})
	running := make(chan struct{})
	runs := 0
	orphanReapFn = func() {
		runs++
		close(running)
		<-release
	}

	returned := make(chan struct{})
	go func() {
		startOrphanReapOnce()
		startOrphanReapOnce() // a second Connect must not queue a second sweep
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("startOrphanReapOnce blocked its caller: the sweep is back on the Connect path")
	}

	select {
	case <-running:
	case <-time.After(5 * time.Second):
		t.Fatal("the sweep never started")
	}
	close(release)

	// Nothing else can start it now: the flag is set and the only in-flight run
	// has been released.
	require.Eventually(t, func() bool { return runs == 1 }, 2*time.Second, 10*time.Millisecond)
	startOrphanReapOnce()
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 1, runs, "the sweep must run at most once per agent-deck run")
}

// cadenceCmdline builds the NUL-separated /proc cmdline of a one-shot cadence
// client targeting an agent-deck session — the argv shape the sweep's scope
// checks are looking for.
func cadenceCmdline(session string) string {
	fields := []string{"tmux", "-L", "sock", "list-clients", "-t", SessionPrefix + session}
	out := ""
	for _, f := range fields {
		out += f + "\x00"
	}
	return out
}

// TestKillStaleControlClients_LaunchesTheOrphanSweep pins the wiring itself.
// The launcher is only reached from killStaleControlClients, and every test
// above drives the sweep's internals directly, so without this one the call at
// the top of killStaleControlClients could be deleted and the whole suite would
// still pass — the sweep would simply never run in production again, which is
// the exact shape of the bug this code was written to fix.
func TestKillStaleControlClients_LaunchesTheOrphanSweep(t *testing.T) {
	originalFn := orphanReapFn
	originalStarted := orphanReapStarted.Load()
	t.Cleanup(func() {
		orphanReapFn = originalFn
		orphanReapStarted.Store(originalStarted)
	})

	launched := make(chan struct{})
	orphanReapFn = func() { close(launched) }
	orphanReapStarted.Store(false)

	// The tmux half of killStaleControlClients fails fast against a socket with
	// no server and is not what this asserts; the launch must happen first and
	// regardless.
	killStaleControlClients("no-such-session", "no-such-socket-for-the-launch-test")

	select {
	case <-launched:
	case <-time.After(5 * time.Second):
		t.Fatal("killStaleControlClients no longer launches the orphan sweep")
	}
}
