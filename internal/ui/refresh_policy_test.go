package ui

import (
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

func TestAdaptiveRefreshMaxSkips(t *testing.T) {
	if got := adaptiveRefreshMaxSkips(0); got != defaultAdaptiveRefreshMaxSkips {
		t.Fatalf("unset = %d, want default %d", got, defaultAdaptiveRefreshMaxSkips)
	}
	if got := adaptiveRefreshMaxSkips(5); got != 5 {
		t.Fatalf("explicit 5 = %d, want 5", got)
	}
	// Negative is the documented kill switch: 0 disables the gate entirely.
	if got := adaptiveRefreshMaxSkips(-1); got != 0 {
		t.Fatalf("negative = %d, want 0 (policy disabled)", got)
	}
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

	ids, ok := h.visibleSessionsForSweep()
	if !ok {
		t.Fatal("freshly published viewport should be usable")
	}
	for _, want := range []string{"a", "b-parent", "b", "d", "e", "z", "attached-1"} {
		if _, found := ids[want]; !found {
			t.Fatalf("visible set missing %q: %v", want, ids)
		}
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
