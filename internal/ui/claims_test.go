package ui

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// newClaimsTestDB opens a fresh migrated StateDB in a temp dir, registers it
// as the package-global DB for the duration of the test, and restores
// whatever was previously global on cleanup so tests can't bleed into each
// other via shared global state.
func newClaimsTestDB(t *testing.T) *statedb.StateDB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := statedb.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	prev := statedb.GetGlobal()
	statedb.SetGlobal(db)
	t.Cleanup(func() {
		statedb.SetGlobal(prev)
		db.Close()
	})
	return db
}

func TestPathInScope(t *testing.T) {
	cases := []struct {
		path, scope string
		want        bool
	}{
		{"fitstars", "", true},                 // empty scope matches all
		{"fitstars", "fitstars", true},         // exact
		{"fitstars/starfit", "fitstars", true}, // child
		{"fitstars2", "fitstars", false},       // prefix but not path-boundary
		{"base", "fitstars", false},
	}
	for _, c := range cases {
		if got := pathInScope(c.path, c.scope); got != c.want {
			t.Errorf("pathInScope(%q,%q) = %v, want %v", c.path, c.scope, got, c.want)
		}
	}
}

func TestSplitInstancesByScope(t *testing.T) {
	mk := func(id, group string) *session.Instance {
		return &session.Instance{ID: id, GroupPath: group}
	}
	in, out := splitInstancesByScope(
		[]*session.Instance{mk("a", "fitstars"), mk("b", "fitstars/starfit"), mk("c", "base")},
		"fitstars",
	)
	if len(in) != 2 || len(out) != 1 || out[0].ID != "c" {
		t.Errorf("split wrong: in=%v out=%v", len(in), len(out))
	}
}

func TestIsOwnedFlagOff(t *testing.T) {
	h := &Home{claimPolling: false}
	if !h.isOwned("anything") {
		t.Error("flag off must own everything (today's behavior)")
	}
}

func TestIsOwnedFlagOn(t *testing.T) {
	h := &Home{claimPolling: true, ownedSessions: map[string]bool{"a": true}}
	if !h.isOwned("a") || h.isOwned("b") {
		t.Error("owned-set gating broken")
	}
}

func TestShouldSweepInstance(t *testing.T) {
	inst := &session.Instance{ID: "a"}
	hOff := &Home{claimPolling: false}
	if !hOff.shouldSweepInstance(inst) {
		t.Error("flag off must sweep everything non-archived")
	}
	hOn := &Home{claimPolling: true, ownedSessions: map[string]bool{"a": true}}
	if !hOn.shouldSweepInstance(inst) {
		t.Error("owned instance must be swept")
	}
	other := &session.Instance{ID: "b"}
	if hOn.shouldSweepInstance(other) {
		t.Error("non-owned, non-orphan-due instance must not be swept")
	}
}

func TestShouldSweepInstanceOrphanDue(t *testing.T) {
	inst := &session.Instance{ID: "orphan-1"}
	h := &Home{
		claimPolling:  true,
		ownedSessions: map[string]bool{"a": true},
		orphanPolled:  map[string]bool{"orphan-1": true},
	}
	if !h.shouldSweepInstance(inst) {
		t.Error("orphan-due instance must be swept so its status gets polled and persisted")
	}
}

func TestOrphanIDs(t *testing.T) {
	now := time.Now().Unix()
	claims := map[string]statedb.ClaimRow{
		"live":  {SessionID: "live", OwnerPID: 1, Heartbeat: now},
		"stale": {SessionID: "stale", OwnerPID: 2, Heartbeat: now - 120},
	}
	all := []string{"live", "stale", "unclaimed"}
	got := orphanIDs(all, claims, 15*time.Second)
	want := map[string]bool{"stale": true, "unclaimed": true}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("unexpected orphan %q", id)
		}
	}
}

// --- Fix B: fail-open degradation, separate orphan set, staleness, lifecycle ---

func TestIsOwnedNilOwnedSessionsFailOpen(t *testing.T) {
	h := &Home{claimPolling: true, ownedSessions: nil}
	if !h.isOwned("anything") {
		t.Error("nil ownedSessions (never reconciled / db unavailable) must fail open and own everything")
	}
}

func TestIsOwnedEmptyMapNotFailOpen(t *testing.T) {
	// A non-nil but empty map is a real reconciled result (we own nothing),
	// distinct from "never reconciled". Must NOT be treated as fail-open.
	h := &Home{claimPolling: true, ownedSessions: map[string]bool{}}
	if h.isOwned("anything") {
		t.Error("empty (but non-nil) owned set must own nothing")
	}
}

func TestIsPolledByMeOwned(t *testing.T) {
	h := &Home{claimPolling: true, ownedSessions: map[string]bool{"a": true}}
	if !h.isPolledByMe("a") {
		t.Error("owned session must be polled")
	}
	if h.isPolledByMe("b") {
		t.Error("neither owned nor orphan-polled session must not be polled")
	}
}

func TestIsPolledByMeOrphan(t *testing.T) {
	h := &Home{
		claimPolling:  true,
		ownedSessions: map[string]bool{"a": true},
		orphanPolled:  map[string]bool{"orphan-1": true},
	}
	if !h.isPolledByMe("a") {
		t.Error("owned session must be polled")
	}
	if !h.isPolledByMe("orphan-1") {
		t.Error("orphan-polled session must be polled")
	}
	if h.isPolledByMe("stranger") {
		t.Error("unrelated session must not be polled")
	}
	// Orphans must never count as owned: pipes/reviver/idle-watch gate on
	// isOwned, not isPolledByMe.
	if h.isOwned("orphan-1") {
		t.Error("orphan must not be reported as owned")
	}
}

func TestIsPolledByMeNilOrphanPolled(t *testing.T) {
	h := &Home{claimPolling: true, ownedSessions: map[string]bool{}, orphanPolled: nil}
	if h.isPolledByMe("x") {
		t.Error("nil orphanPolled must be a safe no-match, not a panic or false-positive")
	}
}

func TestIsPolledByMeFlagOff(t *testing.T) {
	h := &Home{claimPolling: false}
	if !h.isPolledByMe("anything") {
		t.Error("flag off must poll everything")
	}
}

// invalidateNewlyOwnedMemo is the extracted pure helper reconcileClaims uses
// to drop stale lastPersistedStatus memo entries for sessions that just
// transitioned to being owned by this instance.
func TestInvalidateNewlyOwnedMemo(t *testing.T) {
	memo := map[string]string{
		"a": "running", // was owned before, still owned now: memo must survive
		"b": "idle",    // was owned before, no longer owned now: irrelevant either way
		"c": "running", // newly owned this sweep: memo must be dropped
	}
	owned := map[string]bool{"a": true, "c": true}
	prevOwned := map[string]bool{"a": true, "b": true}

	invalidateNewlyOwnedMemo(memo, owned, prevOwned)

	if _, ok := memo["c"]; ok {
		t.Error("memo for newly owned session must be invalidated")
	}
	if _, ok := memo["a"]; !ok {
		t.Error("memo for a session owned before and after must survive")
	}
}

func TestInvalidateNewlyOwnedMemoNilPrevOwned(t *testing.T) {
	// prevOwned nil means "never reconciled before" (fail-open state): every
	// currently owned id counts as newly owned.
	memo := map[string]string{"a": "running"}
	owned := map[string]bool{"a": true}
	invalidateNewlyOwnedMemo(memo, owned, nil)
	if _, ok := memo["a"]; ok {
		t.Error("memo must be invalidated when transitioning from a nil (never-reconciled) prevOwned")
	}
}

func TestClaimStaleAfterExceedsWorstHeartbeatGap(t *testing.T) {
	// Invariant, not a literal: a live owner's heartbeat gap can reach
	// maxStatusInterval (the post-sweep pause) plus the duration of the slow
	// sweep that caused it. claimStaleAfter below 2×maxStatusInterval means a
	// healthy-but-slow owner loses its claims exactly in the degraded scenario
	// the feature was built for.
	if claimStaleAfter < 2*maxStatusInterval {
		t.Errorf("claimStaleAfter = %v, must be >= 2*maxStatusInterval (%v)", claimStaleAfter, 2*maxStatusInterval)
	}
}

func TestReconcileClaimsDBUnavailableFailsOpen(t *testing.T) {
	prev := statedb.GetGlobal()
	statedb.SetGlobal(nil)
	t.Cleanup(func() { statedb.SetGlobal(prev) })

	h := &Home{claimPolling: true}
	h.reconcileClaims([]*session.Instance{{ID: "a"}})

	if h.ownedSessions != nil {
		t.Error("db unavailable must leave ownedSessions nil (fail-open: isOwned treats every session as owned)")
	}
	if !h.isOwned("a") {
		t.Error("with db unavailable, isOwned must fail open")
	}
}

func TestReconcileClaimsFlagOffLeavesFieldsUntouched(t *testing.T) {
	h := &Home{claimPolling: false, orphanPolled: map[string]bool{"x": true}}
	h.reconcileClaims([]*session.Instance{{ID: "a"}})
	if h.orphanPolled == nil || !h.orphanPolled["x"] {
		t.Error("flag off must early-return without touching orphanPolled")
	}
}

func TestReconcileClaimsOrphanSweepSeparateFromOwned(t *testing.T) {
	newClaimsTestDB(t)

	h := &Home{claimPolling: true, groupScope: "scope-a"}
	instances := []*session.Instance{
		{ID: "owned-1", GroupPath: "scope-a"},  // in scope: gets claimed
		{ID: "orphan-1", GroupPath: "scope-b"}, // out of scope: never claimed, no live owner -> orphan
	}

	h.reconcileClaims(instances)

	if !h.isOwned("owned-1") {
		t.Error("in-scope session must be owned after reconcile")
	}
	if h.isOwned("orphan-1") {
		t.Error("out-of-scope orphan must never be reported as owned")
	}
	if !h.isPolledByMe("owned-1") {
		t.Error("owned session must be polled")
	}
	if !h.isPolledByMe("orphan-1") {
		t.Error("orphan session must be polled on the due sweep (this instance elects itself primary)")
	}

	// A second sweep, immediately after, is not due for another orphan pass:
	// orphanPolled must reset to nil so a stale orphan set can't leak forever
	// (and can't poison prevOwned / pipe filters on the next sweep).
	h.reconcileClaims(instances)
	if h.orphanPolled != nil {
		t.Errorf("orphanPolled must reset to nil when the orphan sweep isn't due, got %v", h.orphanPolled)
	}
	if h.isPolledByMe("orphan-1") {
		t.Error("orphan must no longer be polled once the orphan set resets")
	}
	if !h.isOwned("owned-1") {
		t.Error("owned session must remain owned across sweeps")
	}
}

func TestReconcileClaimsHeadlessSkipsOrphanSweep(t *testing.T) {
	newClaimsTestDB(t)

	h := &Home{claimPolling: true, groupScope: "scope-a", headless: true}
	instances := []*session.Instance{
		{ID: "owned-1", GroupPath: "scope-a"},
		{ID: "orphan-1", GroupPath: "scope-b"},
	}

	h.reconcileClaims(instances)

	if !h.isOwned("owned-1") {
		t.Error("in-scope session must still be owned in headless mode")
	}
	if h.orphanPolled != nil {
		t.Errorf("headless instance must never elect primary / orphan-poll, got %v", h.orphanPolled)
	}
	if h.isPolledByMe("orphan-1") {
		t.Error("headless instance must not poll orphans")
	}
}
