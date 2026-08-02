package ui

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Tests for the adaptive refresh policy (refresh-tick v2, issue #1753).
//
// The policy decides whether the background status sweep may skip
// Instance.UpdateStatus() for one session this sweep. Getting it wrong in the
// permissive direction means a session's status silently stops updating, so
// every veto is pinned here, along with the two fail-open paths (no viewport
// snapshot, no cheap evidence) that must degrade to the pre-policy behaviour of
// polling everything.
//
// All of these run without a tmux server, a HOME, or a real Home constructor:
// the decision function is pure and the ledger/viewport helpers touch only
// plain struct fields and the seedable tmux caches.

func quiescentFP() sessionFingerprint {
	return sessionFingerprint{activity: 1000, title: "reviewing diff", command: "node", ok: true}
}

func TestShouldSkipStatusPoll_SkipsQuiescentOffscreenSession(t *testing.T) {
	fp := quiescentFP()
	skip, reason := shouldSkipStatusPoll(skipInput{
		status: session.StatusWaiting, fp: fp, prev: fp, hasPrev: true, maxSkips: 2,
	})
	if !skip {
		t.Fatalf("quiescent off-screen session should be skipped, got poll (%s)", reason)
	}
	if reason != "quiescent" {
		t.Fatalf("reason = %q, want %q", reason, "quiescent")
	}
}

func TestShouldSkipStatusPoll_Vetoes(t *testing.T) {
	fp := quiescentFP()
	changed := fp
	changed.activity = 1001

	cases := []struct {
		name       string
		in         skipInput
		wantReason string
	}{
		{
			name:       "visible rows are never skipped",
			in:         skipInput{visible: true, status: session.StatusWaiting, fp: fp, prev: fp, hasPrev: true, maxSkips: 2},
			wantReason: "visible",
		},
		{
			name:       "error status keeps its own recheck cadence",
			in:         skipInput{status: session.StatusError, fp: fp, prev: fp, hasPrev: true, maxSkips: 2},
			wantReason: "status-unsettled",
		},
		{
			name:       "starting status must converge fast",
			in:         skipInput{status: session.StatusStarting, fp: fp, prev: fp, hasPrev: true, maxSkips: 2},
			wantReason: "status-unsettled",
		},
		{
			name:       "stopped status is not held",
			in:         skipInput{status: session.StatusStopped, fp: fp, prev: fp, hasPrev: true, maxSkips: 2},
			wantReason: "status-unsettled",
		},
		{
			name:       "no baseline yet",
			in:         skipInput{status: session.StatusIdle, fp: fp, maxSkips: 2},
			wantReason: "no-baseline",
		},
		{
			name:       "fingerprint changed",
			in:         skipInput{status: session.StatusRunning, fp: changed, prev: fp, hasPrev: true, maxSkips: 2},
			wantReason: "fingerprint-changed",
		},
		{
			name:       "unusable current fingerprint is no evidence",
			in:         skipInput{status: session.StatusRunning, fp: sessionFingerprint{}, prev: fp, hasPrev: true, maxSkips: 2},
			wantReason: "fingerprint-changed",
		},
		{
			name:       "unusable baseline is no evidence",
			in:         skipInput{status: session.StatusRunning, fp: fp, prev: sessionFingerprint{}, hasPrev: true, maxSkips: 2},
			wantReason: "fingerprint-changed",
		},
		{
			name:       "staleness ceiling reached",
			in:         skipInput{status: session.StatusWaiting, fp: fp, prev: fp, hasPrev: true, skips: 2, maxSkips: 2},
			wantReason: "staleness-ceiling",
		},
		{
			name:       "policy disabled",
			in:         skipInput{status: session.StatusWaiting, fp: fp, prev: fp, hasPrev: true, maxSkips: 0},
			wantReason: "policy-disabled",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			skip, reason := shouldSkipStatusPoll(tc.in)
			if skip {
				t.Fatalf("expected poll, got skip (%s)", reason)
			}
			if reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}

// A quiescent session must be polled at least once every maxSkips+1 sweeps even
// though its fingerprint never changes. This is the bound that keeps the
// time-based transitions inside UpdateStatus (acknowledged -> idle, hook
// freshness expiry, debounce confirmation) from being suppressed indefinitely.
func TestRefreshLedger_PollsAtCeilingDespiteUnchangedFingerprint(t *testing.T) {
	l := newRefreshLedger()
	fp := quiescentFP()

	// Sweep 1 establishes the baseline and must poll.
	if skip, reason := l.decide("s1", fp, session.StatusWaiting, false, 2); skip {
		t.Fatalf("sweep 1 should poll to establish a baseline, got skip (%s)", reason)
	}

	got := make([]bool, 0, 7)
	for i := 0; i < 7; i++ {
		skip, _ := l.decide("s1", fp, session.StatusWaiting, false, 2)
		got = append(got, skip)
	}
	want := []bool{true, true, false, true, true, false, true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("skip pattern = %v, want %v (poll every 3rd sweep)", got, want)
	}
}

func TestRefreshLedger_ChangedFingerprintResetsTheCounter(t *testing.T) {
	l := newRefreshLedger()
	fp := quiescentFP()

	l.decide("s1", fp, session.StatusRunning, false, 2) // baseline
	if skip, _ := l.decide("s1", fp, session.StatusRunning, false, 2); !skip {
		t.Fatal("second sweep on an unchanged fingerprint should skip")
	}

	moved := fp
	moved.activity++
	if skip, reason := l.decide("s1", moved, session.StatusRunning, false, 2); skip {
		t.Fatalf("changed fingerprint must poll, got skip (%s)", reason)
	}
	// The poll rebased the ledger on the new fingerprint, so the counter
	// restarts: two more skips are available before the ceiling.
	for i := 0; i < 2; i++ {
		if skip, reason := l.decide("s1", moved, session.StatusRunning, false, 2); !skip {
			t.Fatalf("post-change sweep %d should skip, got poll (%s)", i+1, reason)
		}
	}
	if skip, _ := l.decide("s1", moved, session.StatusRunning, false, 2); skip {
		t.Fatal("ceiling should force a poll after two post-change skips")
	}
}

func TestRefreshLedger_ForgetForcesAPoll(t *testing.T) {
	l := newRefreshLedger()
	fp := quiescentFP()
	l.decide("s1", fp, session.StatusWaiting, false, 2)
	if skip, _ := l.decide("s1", fp, session.StatusWaiting, false, 2); !skip {
		t.Fatal("precondition: unchanged fingerprint should skip")
	}
	l.forget("s1")
	if skip, reason := l.decide("s1", fp, session.StatusWaiting, false, 2); skip {
		t.Fatalf("forget() must force the next sweep to poll, got skip (%s)", reason)
	}
}

func TestRefreshLedger_PruneDropsDepartedSessions(t *testing.T) {
	l := newRefreshLedger()
	fp := quiescentFP()
	l.decide("keep", fp, session.StatusIdle, false, 2)
	l.decide("gone", fp, session.StatusIdle, false, 2)

	l.prune(map[string]struct{}{"keep": {}})

	l.mu.Lock()
	_, keptKeep := l.entries["keep"]
	_, keptGone := l.entries["gone"]
	l.mu.Unlock()
	if !keptKeep {
		t.Fatal("prune dropped a live session's baseline")
	}
	if keptGone {
		t.Fatal("prune kept a departed session's baseline")
	}
}

// A nil ledger (alternate/test Home constructors never build one) must behave
// like the policy does not exist: poll everything, never panic.
func TestRefreshLedger_NilReceiverAlwaysPolls(t *testing.T) {
	var l *refreshLedger
	if skip, reason := l.decide("s1", quiescentFP(), session.StatusWaiting, false, 2); skip {
		t.Fatalf("nil ledger must poll, got skip (%s)", reason)
	}
	l.forget("s1")
	l.prune(nil)
}

// ---- fingerprint ----

func TestFingerprintSession_UsesCachesAndTracksPaneTitle(t *testing.T) {
	const tmuxName = "agentdeck-fp-test-A"
	inst := instWithTmuxName(t, "fp-A", tmuxName)
	h := newHomeForSnapshotTest()

	tmux.SeedSessionActivityCacheForTest(t, map[string]int64{tmuxName: 4242})
	tmux.SeedPaneInfoCacheForTest(t, map[string]tmux.PaneInfo{
		tmuxName: {Title: "task one", CurrentCommand: "node"},
	})

	first := h.fingerprintSession(inst)
	if !first.ok {
		t.Fatal("fingerprint should be usable when both caches are fresh")
	}
	if first.activity != 4242 || first.title != "task one" || first.command != "node" {
		t.Fatalf("fingerprint = %+v, want activity 4242 / title \"task one\" / command \"node\"", first)
	}
	if second := h.fingerprintSession(inst); !second.unchangedFrom(first) {
		t.Fatalf("re-fingerprinting an unchanged session must match: %+v vs %+v", second, first)
	}

	// A new pane title alone (same activity second) must break the match: the
	// OSC title is how Claude signals spinner/done transitions.
	tmux.SeedPaneInfoCacheForTest(t, map[string]tmux.PaneInfo{
		tmuxName: {Title: "task two", CurrentCommand: "node"},
	})
	if retitled := h.fingerprintSession(inst); retitled.unchangedFrom(first) {
		t.Fatal("a changed pane title must produce a different fingerprint")
	}
}

func TestFingerprintSession_NoEvidenceWhenCachesMissing(t *testing.T) {
	const tmuxName = "agentdeck-fp-test-B"
	inst := instWithTmuxName(t, "fp-B", tmuxName)
	h := newHomeForSnapshotTest()

	// Activity cache seeded, pane cache absent -> no evidence.
	tmux.SeedSessionActivityCacheForTest(t, map[string]int64{tmuxName: 7})
	if fp := h.fingerprintSession(inst); fp.ok {
		t.Fatal("missing pane info must yield an unusable fingerprint")
	}

	// Pane cache present but stale -> still no evidence.
	tmux.SeedPaneInfoCacheForTest(t, map[string]tmux.PaneInfo{tmuxName: {Title: "x"}})
	tmux.ExpirePaneInfoCacheForTest(t)
	if fp := h.fingerprintSession(inst); fp.ok {
		t.Fatal("stale pane cache must yield an unusable fingerprint")
	}

	// A session with no tmux session at all -> no evidence.
	if fp := h.fingerprintSession(&session.Instance{ID: "no-tmux"}); fp.ok {
		t.Fatal("instance without a tmux session must yield an unusable fingerprint")
	}
	if fp := h.fingerprintSession(nil); fp.ok {
		t.Fatal("nil instance must yield an unusable fingerprint")
	}
}

// ---- visible-row hold + budget (issue #1753 group-expand case) ----

// A VISIBLE row with a settled status and an unchanged fingerprint is held
// under the same ceiling as an off-screen row: hold, hold, then forced poll.
// This is what makes an expanded 60-row group cost ~zero when quiescent.
func TestRefreshLedger_HoldVisibleRespectsCeiling(t *testing.T) {
	l := newRefreshLedger()
	fp := quiescentFP()
	l.admitPoll("v1", fp) // baseline from a real poll

	for i := 0; i < 2; i++ {
		if !l.holdVisible("v1", fp, session.StatusWaiting, 2) {
			t.Fatalf("hold %d: settled visible row with unchanged fingerprint should be held", i+1)
		}
	}
	if l.holdVisible("v1", fp, session.StatusWaiting, 2) {
		t.Fatal("ceiling reached: third consecutive hold must be refused")
	}
	// The refused hold makes the row a budget candidate; an admitted poll
	// rebases and the cycle restarts.
	l.admitPoll("v1", fp)
	if !l.holdVisible("v1", fp, session.StatusWaiting, 2) {
		t.Fatal("post-poll hold should be available again")
	}
}

func TestRefreshLedger_HoldVisibleVetoes(t *testing.T) {
	l := newRefreshLedger()
	fp := quiescentFP()
	changed := fp
	changed.activity++
	l.admitPoll("v1", fp)

	if l.holdVisible("v1", changed, session.StatusWaiting, 2) {
		t.Fatal("changed fingerprint must not be held")
	}
	if l.holdVisible("v1", sessionFingerprint{}, session.StatusWaiting, 2) {
		t.Fatal("unusable fingerprint must not be held")
	}
	if l.holdVisible("v1", fp, session.StatusStarting, 2) {
		t.Fatal("unsettled status must not be held")
	}
	if l.holdVisible("v1", fp, session.StatusWaiting, 0) {
		t.Fatal("kill switch (maxSkips<=0) must not hold")
	}
	if l.holdVisible("no-baseline", fp, session.StatusWaiting, 2) {
		t.Fatal("row without a polled baseline must not be held")
	}
	var nilLedger *refreshLedger
	if nilLedger.holdVisible("v1", fp, session.StatusWaiting, 2) {
		t.Fatal("nil ledger must never hold")
	}
	// None of the refused holds may have advanced the ceiling counter.
	if !l.holdVisible("v1", fp, session.StatusWaiting, 1) {
		t.Fatal("refused holds must not consume the ceiling (skips should still be 0)")
	}
}

// heldSteady is the read-only hold used by processStatusUpdate's visible pass.
// It must never advance the ceiling counter — the background sweep owns
// freshness — so an arbitrary number of held passes leaves the sweep-side
// hold/poll pattern untouched.
func TestRefreshLedger_HeldSteadyIsReadOnly(t *testing.T) {
	l := newRefreshLedger()
	fp := quiescentFP()
	changed := fp
	changed.title = "typing"
	l.admitPoll("v1", fp)

	for i := 0; i < 50; i++ {
		if !l.heldSteady("v1", fp, session.StatusIdle) {
			t.Fatalf("pass %d: steady visible row should be held read-only", i)
		}
	}
	if l.heldSteady("v1", changed, session.StatusIdle) {
		t.Fatal("changed fingerprint must not be held")
	}
	if l.heldSteady("v1", fp, session.StatusError) {
		t.Fatal("unsettled status must not be held")
	}
	var nilLedger *refreshLedger
	if nilLedger.heldSteady("v1", fp, session.StatusIdle) {
		t.Fatal("nil ledger must never hold")
	}
	// 50 read-only holds consumed none of the sweep ceiling: both sweep-side
	// holds must still be available.
	for i := 0; i < 2; i++ {
		if !l.holdVisible("v1", fp, session.StatusIdle, 2) {
			t.Fatalf("sweep hold %d should still be available after read-only holds", i+1)
		}
	}
}

// The budget must cycle: over ceil(due/budget) sweeps every due visible row is
// polled at least once, because deferred rows age (deferrals++) and admission
// orders most-starved first.
func TestAdmitVisiblePolls_RoundRobinCoversAllDueRows(t *testing.T) {
	const due, budget = 25, 10
	l := newRefreshLedger()
	fp := quiescentFP()
	insts := make([]*session.Instance, due)
	for i := range insts {
		insts[i] = &session.Instance{ID: string(rune('A' + i))}
	}

	polled := make(map[string]int)
	sweeps := (due + budget - 1) / budget // 3
	for s := 0; s < sweeps; s++ {
		candidates := make([]visiblePollCandidate, 0, due)
		for _, inst := range insts {
			candidates = append(candidates, visiblePollCandidate{
				inst: inst, fp: fp, deferrals: l.deferralCount(inst.ID),
			})
		}
		// maxSkips huge: never large enough to force admission within these
		// sweeps, so this test stays a pure round-robin check (the
		// maxSkips-as-hard-floor behaviour has its own test below).
		admitted, deferred := admitVisiblePolls(candidates, "", budget, 1000)
		if len(admitted) != budget {
			t.Fatalf("sweep %d admitted %d rows, want exactly the budget %d", s, len(admitted), budget)
		}
		for _, c := range admitted {
			l.admitPoll(c.inst.ID, c.fp)
			polled[c.inst.ID]++
		}
		for _, c := range deferred {
			l.deferPoll(c.inst.ID)
		}
	}
	for _, inst := range insts {
		if polled[inst.ID] == 0 {
			t.Fatalf("row %s was never polled in %d sweeps (budget starvation)", inst.ID, sweeps)
		}
	}
}

// The cursor row is what the preview pane shows: it must always be in the
// admitted prefix, even when it is the least-starved candidate.
func TestAdmitVisiblePolls_CursorRowIsNeverDeferred(t *testing.T) {
	candidates := make([]visiblePollCandidate, 0, 15)
	for i := 0; i < 15; i++ {
		candidates = append(candidates, visiblePollCandidate{
			inst: &session.Instance{ID: string(rune('a' + i))}, deferrals: 5,
		})
	}
	candidates = append(candidates, visiblePollCandidate{
		inst: &session.Instance{ID: "cursor-row"}, deferrals: 0,
	})

	admitted, deferred := admitVisiblePolls(candidates, "cursor-row", 10, 1000)
	for _, c := range deferred {
		if c.inst.ID == "cursor-row" {
			t.Fatal("cursor row was deferred by the budget")
		}
	}
	if len(admitted) == 0 || admitted[0].inst.ID != "cursor-row" {
		t.Fatalf("cursor row should be admitted first, got %v", admitted)
	}
}

// TestAdmitVisiblePolls_MaxSkipsBoundsStalenessRegardlessOfDueCount proves
// the fix for the review's second HIGH finding: round-robin priority alone
// only bounds staleness by ceil(due/budget) — a function of how many rows
// happen to be due at once, not of maxSkips. Before this fix, a due set far
// larger than budget*maxSkips could push an individual row's wait
// arbitrarily past the "at most maxSkips sweeps stale" ceiling the design
// doc promises for visible rows (e.g. 100 due rows at budget 10 and
// maxSkips 2: the tail could wait up to 9 sweeps, not 2).
//
// This drives many more due rows than budget*maxSkips would allow under a
// pure round-robin and asserts NO row is ever deferred more than maxSkips
// consecutive times before being admitted — maxSkips must be a hard floor
// that overrides the budget, not just a priority hint.
func TestAdmitVisiblePolls_MaxSkipsBoundsStalenessRegardlessOfDueCount(t *testing.T) {
	const due, budget, maxSkips = 137, 10, 3 // ceil(due/budget) = 14 sweeps under pure round-robin >> maxSkips
	l := newRefreshLedger()
	fp := quiescentFP()
	insts := make([]*session.Instance, due)
	for i := range insts {
		insts[i] = &session.Instance{ID: fmt.Sprintf("row-%03d", i)}
	}

	streak := make(map[string]int) // consecutive sweeps each row has been deferred
	everAdmitted := make(map[string]bool)

	for s := 0; s < 40; s++ { // far more sweeps than either bound would need
		candidates := make([]visiblePollCandidate, 0, due)
		for _, inst := range insts {
			candidates = append(candidates, visiblePollCandidate{
				inst: inst, fp: fp, deferrals: l.deferralCount(inst.ID),
			})
		}
		admitted, deferred := admitVisiblePolls(candidates, "", budget, maxSkips)
		for _, c := range admitted {
			l.admitPoll(c.inst.ID, c.fp)
			if streak[c.inst.ID] > maxSkips {
				t.Fatalf("sweep %d: row %s waited %d consecutive sweeps before admission, want <= %d (maxSkips)",
					s, c.inst.ID, streak[c.inst.ID], maxSkips)
			}
			streak[c.inst.ID] = 0
			everAdmitted[c.inst.ID] = true
		}
		for _, c := range deferred {
			l.deferPoll(c.inst.ID)
			streak[c.inst.ID]++
			if streak[c.inst.ID] > maxSkips {
				t.Fatalf("sweep %d: row %s has been deferred %d consecutive times without being admitted, want <= %d (maxSkips)",
					s, c.inst.ID, streak[c.inst.ID], maxSkips)
			}
		}
	}
	for _, inst := range insts {
		if !everAdmitted[inst.ID] {
			t.Fatalf("row %s was never admitted across 40 sweeps", inst.ID)
		}
	}
}

// TestAdmitVisiblePolls_MaxSkipsDisabledFallsBackToPureRoundRobin pins the
// maxSkips<=0 branch: with the adaptive policy off (kill switch, or a
// failed-open sweep) forced admission must not fire — callers already keep
// visibleDue empty in that state, but the function itself must not depend on
// that to stay safe.
func TestAdmitVisiblePolls_MaxSkipsDisabledFallsBackToPureRoundRobin(t *testing.T) {
	candidates := make([]visiblePollCandidate, 0, 20)
	for i := 0; i < 20; i++ {
		candidates = append(candidates, visiblePollCandidate{
			inst: &session.Instance{ID: fmt.Sprintf("row-%d", i)}, deferrals: 999,
		})
	}
	admitted, deferred := admitVisiblePolls(candidates, "", 5, 0)
	if len(admitted) != 5 {
		t.Fatalf("maxSkips<=0 must not force extra admissions: admitted = %d, want budget 5", len(admitted))
	}
	if len(deferred) != 15 {
		t.Fatalf("maxSkips<=0 must not force extra admissions: deferred = %d, want 15", len(deferred))
	}
}

// TestRefreshLedger_PipeIdleSkip_BoundsTheQuietPipeShortcut proves the fix
// for the review's first HIGH finding: the sweep's PipeManager quiet-pipe
// shortcut used to skip a session unconditionally for as long as
// PipeManager reported no output, never reaching decide()/holdVisible() and
// therefore never refreshing the session's ledger baseline — so
// heldSteady() (the incremental visible-first path's read-only hold) could
// hold that session forever. pipeIdleSkip shares the same skip counter and
// ceiling as the fingerprint gate, so the shortcut can no longer hold a
// session past maxSkips consecutive sweeps.
func TestRefreshLedger_PipeIdleSkip_BoundsTheQuietPipeShortcut(t *testing.T) {
	l := newRefreshLedger()
	const maxSkips = 2

	// A pipe that reports "no new output" every single sweep, indefinitely,
	// must still eventually be forced open — the ceiling, not PipeManager,
	// has the final say.
	for round := 0; round < 5; round++ {
		for i := 0; i < maxSkips; i++ {
			if !l.pipeIdleSkip("piped-1", maxSkips) {
				t.Fatalf("round %d, sweep %d: expected a skip (still under the ceiling)", round, i)
			}
		}
		if l.pipeIdleSkip("piped-1", maxSkips) {
			t.Fatalf("round %d: pipeIdleSkip must return false at the ceiling — a piped session cannot be held forever", round)
		}
		// The forced-open sweep resets the entry to a mismatching (unusable)
		// baseline: the caller's fallthrough to decide()/holdVisible() is
		// therefore guaranteed to see "fingerprint-changed" and actually poll.
		entry, ok := ledgerEntry(l, "piped-1")
		if !ok {
			t.Fatalf("round %d: forced-open must leave a (reset) entry, not delete it", round)
		}
		if entry.skips != 0 || entry.fp.ok {
			t.Fatalf("round %d: forced-open entry not reset: %+v", round, entry)
		}
	}
}

// TestRefreshLedger_PipeIdleSkip_KillSwitchIsUnconditional pins the
// maxSkips<=0 branch: PipeManager alone decides, matching the pre-adaptive-
// policy shortcut byte-for-byte (skip forever, no forced poll) — this
// function must never introduce a ceiling the kill switch didn't ask for.
func TestRefreshLedger_PipeIdleSkip_KillSwitchIsUnconditional(t *testing.T) {
	l := newRefreshLedger()
	for i := 0; i < 50; i++ {
		if !l.pipeIdleSkip("piped-killswitch", 0) {
			t.Fatalf("sweep %d: maxSkips<=0 must always skip (pre-policy behaviour)", i)
		}
	}
}

func TestRefreshLedger_PipeIdleSkip_NilSafety(t *testing.T) {
	var nilLedger *refreshLedger
	if !nilLedger.pipeIdleSkip("x", 2) {
		t.Fatal("nil ledger must behave like the pre-policy shortcut: always skip")
	}
	l := newRefreshLedger()
	if !l.pipeIdleSkip("", 2) {
		t.Fatal("empty id must always skip (matches every other nil/empty-safe ledger method)")
	}
}

// The hook and SSE UpdatedAt inputs are what make the "real transitions are
// not delayed" argument true for event-driven tools: a Stop /Notification/
// PermissionRequest hook (or an OpenCode /event sample) moves the fingerprint
// even when the tmux-side evidence (activity/title/command) is unchanged, so
// the very next sweep polls instead of holding the skip. Pinned here because
// fingerprintSession is the ONLY reader of these inputs — nothing else fails
// if they are dropped.
func TestFingerprintSession_HookAndSSEInputsMoveTheFingerprint(t *testing.T) {
	const tmuxName = "agentdeck-fp-test-C"
	inst := instWithTmuxName(t, "fp-C", tmuxName)
	h := newHomeForSnapshotTest()
	h.hookWatcher = session.NewStatusFileWatcherForTest(t)
	h.sseWatcher = session.NewOpenCodeSSEWatcher(nil)

	tmux.SeedSessionActivityCacheForTest(t, map[string]int64{tmuxName: 4242})
	tmux.SeedPaneInfoCacheForTest(t, map[string]tmux.PaneInfo{
		tmuxName: {Title: "steady", CurrentCommand: "node"},
	})

	base := h.fingerprintSession(inst)
	if !base.ok {
		t.Fatal("fingerprint should be usable with fresh tmux caches")
	}
	if base.hookAt != 0 || base.sseAt != 0 {
		t.Fatalf("no watcher entries yet: hookAt/sseAt should be 0, got %+v", base)
	}
	if again := h.fingerprintSession(inst); !again.unchangedFrom(base) {
		t.Fatalf("steady state must re-fingerprint identically: %+v vs %+v", again, base)
	}

	// A Stop hook lands. tmux evidence is untouched, but hookAt must move the
	// fingerprint so the next sweep polls the transition immediately.
	hookTime := time.Now()
	h.hookWatcher.SetHookStatusForTest(t, inst.ID, &session.HookStatus{
		Status: "waiting", Event: "Stop", UpdatedAt: hookTime,
	})
	withHook := h.fingerprintSession(inst)
	if withHook.unchangedFrom(base) {
		t.Fatal("a delivered hook must change the fingerprint even with unchanged tmux evidence")
	}
	if withHook.hookAt != hookTime.UnixNano() {
		t.Fatalf("hookAt = %d, want the hook's UpdatedAt %d", withHook.hookAt, hookTime.UnixNano())
	}

	// A re-delivered hook with identical content but a newer UpdatedAt is a
	// new event and must move the fingerprint again.
	h.hookWatcher.SetHookStatusForTest(t, inst.ID, &session.HookStatus{
		Status: "waiting", Event: "Stop", UpdatedAt: hookTime.Add(time.Second),
	})
	rehooked := h.fingerprintSession(inst)
	if rehooked.unchangedFrom(withHook) {
		t.Fatal("a re-delivered hook with a newer UpdatedAt must change the fingerprint")
	}

	// An OpenCode SSE sample moves sseAt the same way (issue #1614).
	sseTime := time.Now()
	h.sseWatcher.SetStatusForTest(t, inst.ID, &session.OpenCodeSSEStatus{
		Status: "running", UpdatedAt: sseTime,
	})
	withSSE := h.fingerprintSession(inst)
	if withSSE.unchangedFrom(rehooked) {
		t.Fatal("an SSE status sample must change the fingerprint")
	}
	if withSSE.sseAt != sseTime.UnixNano() {
		t.Fatalf("sseAt = %d, want the sample's UpdatedAt %d", withSSE.sseAt, sseTime.UnixNano())
	}

	// Back to steady state: with all inputs unchanged the fingerprint is
	// stable again, i.e. the session is once more eligible for the skip.
	if settled := h.fingerprintSession(inst); !settled.unchangedFrom(withSSE) {
		t.Fatalf("unchanged hook+SSE inputs must re-fingerprint identically: %+v vs %+v", settled, withSSE)
	}
}

// ---- viewport publication ----

func TestPublishVisibleSessions_CoversViewportAndCursor(t *testing.T) {
	h := &Home{height: 14} // visibleRowBudget() = 6
	inA := &session.Instance{ID: "a"}
	inB := &session.Instance{ID: "b"}
	offscreen := &session.Instance{ID: "z"}
	h.flatItems = []session.Item{
		{Type: session.ItemTypeSession, Session: inA},
		{Type: session.ItemTypeWindow, WindowSessionID: "b-parent"},
		{Type: session.ItemTypeSession, Session: inB},
		{Type: session.ItemTypeGroup},
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "d"}},
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "e"}},
		{Type: session.ItemTypeSession, Session: offscreen},
	}
	h.viewOffset = 0
	h.cursor = 6 // selected row is below the viewport window

	h.publishVisibleSessions("attached-1")

	snap, ok := h.visibleSessionsForSweep()
	if !ok {
		t.Fatal("freshly published viewport should be usable")
	}
	for _, want := range []string{"a", "b-parent", "b", "d", "e", "z", "attached-1"} {
		if _, found := snap.ids[want]; !found {
			t.Fatalf("visible set missing %q: %v", want, snap.ids)
		}
	}
	// The selected row is published as the cursor ID so the sweep's visible
	// budget can never defer the row the preview pane is showing.
	if snap.cursorID != "z" {
		t.Fatalf("cursorID = %q, want %q (the selected row)", snap.cursorID, "z")
	}
}

func TestVisibleSessionsForSweep_FailsOpen(t *testing.T) {
	// Never published: the sweep must not trust an absent snapshot.
	h := &Home{}
	if _, ok := h.visibleSessionsForSweep(); ok {
		t.Fatal("unpublished viewport must report unusable so the sweep polls everything")
	}

	// Published but older than visibleSessionsMaxAge (TUI suspended by
	// tea.Exec, or the event loop wedged): also unusable.
	h.visibleSessions.Store(visibleSessionSnapshot{
		ids: map[string]struct{}{"a": {}},
		at:  time.Now().Add(-2 * visibleSessionsMaxAge),
	})
	if _, ok := h.visibleSessionsForSweep(); ok {
		t.Fatal("stale viewport must report unusable so the sweep polls everything")
	}
}

// ---- render snapshot generation skip ----

// refreshSessionRenderSnapshot is called from several sweep/tick paths and used
// to allocate and publish a fresh N-entry map every time. When nothing changed
// it must publish nothing at all — asserted by map identity, since a rebuild
// necessarily produces a different map.
func TestRefreshSessionRenderSnapshot_SkipsRebuildWhenUnchanged(t *testing.T) {
	const tmuxName = "agentdeck-gen-skip-A"
	inst := instWithTmuxName(t, "gen-A", tmuxName)
	h := newHomeForSnapshotTest()

	tmux.SeedPaneInfoCacheForTest(t, map[string]tmux.PaneInfo{tmuxName: {Title: "task one"}})
	h.refreshSessionRenderSnapshot([]*session.Instance{inst})
	first := h.getSessionRenderSnapshot()
	if first[inst.ID].paneTitle != "task one" {
		t.Fatalf("first refresh: paneTitle = %q, want %q", first[inst.ID].paneTitle, "task one")
	}

	h.refreshSessionRenderSnapshot([]*session.Instance{inst})
	if second := h.getSessionRenderSnapshot(); mapIdentity(second) != mapIdentity(first) {
		t.Fatal("unchanged refresh republished a new snapshot map instead of skipping")
	}

	// A real change must still land.
	tmux.SeedPaneInfoCacheForTest(t, map[string]tmux.PaneInfo{tmuxName: {Title: "task two"}})
	h.refreshSessionRenderSnapshot([]*session.Instance{inst})
	third := h.getSessionRenderSnapshot()
	if mapIdentity(third) == mapIdentity(first) {
		t.Fatal("changed pane title should have republished the snapshot")
	}
	if third[inst.ID].paneTitle != "task two" {
		t.Fatalf("paneTitle = %q, want %q", third[inst.ID].paneTitle, "task two")
	}
}

// A session leaving the deck changes the snapshot's membership even when every
// surviving entry is byte-identical, so the rebuild must not be skipped.
func TestRefreshSessionRenderSnapshot_RebuildsWhenSessionCountChanges(t *testing.T) {
	const nameA, nameB = "agentdeck-gen-skip-B1", "agentdeck-gen-skip-B2"
	instA := instWithTmuxName(t, "gen-B1", nameA)
	instB := instWithTmuxName(t, "gen-B2", nameB)
	h := newHomeForSnapshotTest()

	tmux.SeedPaneInfoCacheForTest(t, map[string]tmux.PaneInfo{
		nameA: {Title: "task one"},
		nameB: {Title: "task two"},
	})
	h.refreshSessionRenderSnapshot([]*session.Instance{instA, instB})
	if got := len(h.getSessionRenderSnapshot()); got != 2 {
		t.Fatalf("snapshot size = %d, want 2", got)
	}

	h.refreshSessionRenderSnapshot([]*session.Instance{instA})
	snap := h.getSessionRenderSnapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot size after removal = %d, want 1 (%v)", len(snap), snap)
	}
	if _, stillThere := snap[instB.ID]; stillThere {
		t.Fatal("removed session still present in the snapshot")
	}
}

func mapIdentity(m map[string]sessionRenderState) uintptr {
	return reflect.ValueOf(m).Pointer()
}
