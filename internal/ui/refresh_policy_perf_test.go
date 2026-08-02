package ui

import (
	"fmt"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/testutil"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Perf gate for the adaptive-refresh sweep's PER-SESSION DECISION cost (PR
// #1765, issue #1753) — fingerprintSession + refreshLedger.decide /
// holdVisible + admitVisiblePolls, the code that runs for every live
// session on every background sweep and every incremental visible-first
// pass (home.go backgroundStatusUpdate / processStatusUpdate).
//
// What this test does NOT gate: the thing the PR description's "Measured
// effect" table reports a percentage for — real Instance.UpdateStatus()
// cost (tmux capture-pane subprocesses) under a live multi-session tmux
// fleet. That number came from an out-of-band sandboxed 60-session tmux
// harness, which is inherently COLD, real-subprocess, real-tmux-server work
// that does not belong in a hermetic, tmux-free CI gate, and this repo has
// no in-CI harness that reproduces it. The PR does NOT re-assert that
// number as a CI-verified claim; it is unquantified on the rebased head
// pending such a harness (see the PR description).
//
// What THIS test DOES gate: the decision layer's own claim of being "a
// handful of map lookups, spawns nothing" (refresh_policy.go's package
// doc). It is pure in-process Go — seeded tmux caches, no tmux server, no
// subprocess — so a regression that reintroduces a spawn, a lock held
// across the whole sweep, or an accidental O(n^2) in the admission sort
// would show up here as a walltime blowout, even though such a regression
// would very likely touch no cmd/agent-deck/*.go line and so would
// otherwise be invisible to this repo's perf gate (see
// .github/workflows/perf-smoke.yml's internal/ui/** path filter, added
// alongside this test so this package's own hot path is actually covered).
//
// Classified WARM: pure in-process Go against seeded package-level caches,
// no process/syscall boundary. See docs/perf-budget-suite.md.

// perfSweepDecisionN is the per-iteration session count: roughly 3x the
// #1753 diagnostic's 60-session reference fleet, comfortably clearing the
// 1ms budget floor without inflating the timed loop's own allocation noise.
const perfSweepDecisionN = 200

// perfSweepDecisionBase is an ENGINEERING ESTIMATE, not a locally-measured
// median. This development host's safety rules forbid running `go test`
// against this repo at all, sandboxed or not (two prior fleet-data-loss
// incidents from un-isolated runs), so the docs/perf-budget-suite.md
// convention of citing an observed local median could not be followed from
// here — CI is the first place this test will actually execute.
//
// 5ms budgeted for perfSweepDecisionN=200 pure map-lookup-and-compare
// decisions (no I/O, no allocation-heavy work, no subprocess) is generous
// by roughly an order of magnitude on any modern CI runner: the intent is a
// gate wide enough not to flake on shared-runner variance while still
// catching a real regression by a large margin (e.g. a reintroduced
// per-session subprocess would cost roughly 200x a few milliseconds each,
// blowing through this budget 100x+ over). Tighten this to a cited value
// from the perf-medians.txt artifact (uploaded by
// .github/workflows/perf-smoke.yml) the first time this test runs green in
// CI, per the RFC-required process for loosening — or tightening — a
// TestPerf_* budget.
const perfSweepDecisionBase = 5 * time.Millisecond // WarmBudget = 15ms local, 30ms CI (multiplier=2.0)

func TestPerf_AdaptiveRefreshSweepDecision(t *testing.T) {
	testutil.SkipIfShort(t)

	h := newHomeForSnapshotTest()
	l := newRefreshLedger()
	const maxSkips = 2

	insts := make([]*session.Instance, perfSweepDecisionN)
	paneInfo := make(map[string]tmux.PaneInfo, perfSweepDecisionN)
	activity := make(map[string]int64, perfSweepDecisionN)
	for i := range insts {
		name := fmt.Sprintf("agentdeck-perf-sweep-%03d", i)
		insts[i] = instWithTmuxName(t, fmt.Sprintf("perf-sweep-%03d", i), name)
		paneInfo[name] = tmux.PaneInfo{Title: "steady", CurrentCommand: "node"}
		activity[name] = int64(1000 + i)
	}
	tmux.SeedPaneInfoCacheForTest(t, paneInfo)
	tmux.SeedSessionActivityCacheForTest(t, activity)

	budget := testutil.WarmBudget(t, perfSweepDecisionBase)

	// One simulated sweep: half the sessions off-screen (through decide()),
	// half visible (through holdVisible() + the budget admission pass) —
	// mirrors the real split in backgroundStatusUpdate.
	got := testutil.TrimmedMeanWarm(func() {
		due := make([]visiblePollCandidate, 0, perfSweepDecisionN/2)
		for i, inst := range insts {
			fp := h.fingerprintSession(inst)
			if i%2 == 0 {
				l.decide(inst.ID, fp, session.StatusWaiting, false, maxSkips)
				continue
			}
			if l.holdVisible(inst.ID, fp, session.StatusWaiting, maxSkips) {
				continue
			}
			due = append(due, visiblePollCandidate{inst: inst, fp: fp, deferrals: l.deferralCount(inst.ID)})
		}
		admitted, deferred := admitVisiblePolls(due, "", visiblePollBudgetPerSweep, maxSkips)
		for _, c := range admitted {
			l.admitPoll(c.inst.ID, c.fp)
		}
		for _, c := range deferred {
			l.deferPoll(c.inst.ID)
		}
	})

	if got > budget {
		t.Fatalf("adaptive-refresh sweep decision (%d sessions, fingerprint+decide/holdVisible+admitVisiblePolls) trimmed mean = %v, budget = %v (regression in the refresh_policy.go decision layer)",
			perfSweepDecisionN, got, budget)
	}
	t.Logf("adaptive-refresh sweep decision (%d sessions) trimmed mean = %v (budget = %v)", perfSweepDecisionN, got, budget)
}
