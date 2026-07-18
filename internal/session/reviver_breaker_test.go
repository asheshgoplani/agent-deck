package session

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"
)

var errTest = errors.New("revive action failed")

// --- test scaffolding -------------------------------------------------------
// (fakeClock is defined in issue1143_idle_timeout_test.go: Now()/Advance().)

// recordingHandler captures slog records for assertions.
type recordingHandler struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recs = append(h.recs, r)
	return nil
}
func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

func (h *recordingHandler) count(msg string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, r := range h.recs {
		if r.Message == msg {
			n++
		}
	}
	return n
}

// newTestBreaker returns a breaker driven by a fake clock and a recording log.
func newTestBreaker() (*ReviveBreaker, *fakeClock, *recordingHandler) {
	clk := newFakeClock(time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC))
	h := &recordingHandler{}
	b := NewReviveBreaker(slog.New(h))
	b.now = clk.Now
	return b, clk, h
}

// wedgedReviver builds a Reviver that simulates a wedged tmux server: the
// server answers has-session (TmuxExists=true) and the pipe is always dead
// (PipeAlive=false), while the revive action "succeeds" (Connect returns nil)
// but never actually stabilizes the session. calls counts revive actions.
func wedgedReviver(b *ReviveBreaker, calls *int) *Reviver {
	return &Reviver{
		TmuxExists:   func(string, string) bool { return true },
		PipeAlive:    func(string) bool { return false },
		ReviveAction: func(*Instance) error { *calls++; return nil },
		Stagger:      0,
		Breaker:      b,
	}
}

// sweep runs one ReviveAll over the given instances and returns the outcomes.
func sweep(r *Reviver, insts []*Instance) []ReviveOutcome {
	return r.ReviveAll(insts)
}

// --- tests ------------------------------------------------------------------

// TestReviveBreaker_TripsAfterThreshold verifies the circuit opens after
// FutilityThreshold futile revives and then skips further attempts.
func TestReviveBreaker_TripsAfterThreshold(t *testing.T) {
	b, _, h := newTestBreaker()
	calls := 0
	r := wedgedReviver(b, &calls)
	inst := newReviverTestInstance("wedged", StatusError)

	// Sweep 1: attempt (pending set). Sweeps 2,3: futile #1,#2 + attempt.
	// Sweep 4: futile #3 → circuit opens → skip.
	var lastOut ReviveOutcome
	for i := 0; i < 6; i++ {
		outs := sweep(r, []*Instance{inst})
		lastOut = outs[0]
	}

	// Attempts should have stopped at 3 (sweeps 1-3); sweeps 4-6 skipped.
	if calls != 3 {
		t.Fatalf("expected revive action bounded to 3 calls, got %d", calls)
	}
	if !lastOut.CircuitOpen {
		t.Errorf("expected CircuitOpen=true once tripped")
	}
	if h.count("reviver_circuit_open") == 0 {
		t.Errorf("expected a reviver_circuit_open log")
	}
	if got := b.openCircuits(); got != 1 {
		t.Errorf("expected 1 open circuit, got %d", got)
	}
}

// TestReviveBreaker_ExponentialBackoffAndProbe verifies that after the circuit
// opens, exactly one probe fires per cooldown expiry and a re-futile probe
// doubles the cooldown.
func TestReviveBreaker_ExponentialBackoffAndProbe(t *testing.T) {
	b, clk, _ := newTestBreaker()
	calls := 0
	r := wedgedReviver(b, &calls)
	inst := newReviverTestInstance("wedged", StatusError)

	// Trip the circuit (4 sweeps → 3 attempts, then open).
	for i := 0; i < 4; i++ {
		sweep(r, []*Instance{inst})
	}
	if calls != 3 {
		t.Fatalf("setup: expected 3 attempts pre-open, got %d", calls)
	}

	// While cooling down (< InitialCooldown), extra sweeps must NOT attempt.
	clk.Advance(1 * time.Minute)
	sweep(r, []*Instance{inst})
	if calls != 3 {
		t.Fatalf("expected no probe during cooldown, got %d calls", calls)
	}

	// Cooldown expired → exactly one probe.
	clk.Advance(2 * time.Minute) // now past InitialCooldown (2m)
	sweep(r, []*Instance{inst})
	if calls != 4 {
		t.Fatalf("expected one probe at cooldown expiry, got %d calls", calls)
	}

	// The probe was futile (still errored next sweep) → cooldown doubled to 4m.
	// At +2m it must still be cooling down.
	clk.Advance(2 * time.Minute)
	sweep(r, []*Instance{inst}) // detects futile probe, re-arms; no attempt
	if calls != 4 {
		t.Fatalf("expected no probe yet (backoff doubled), got %d calls", calls)
	}
	// Advance past the doubled window (cooldown re-armed to 4m at detection
	// time) and probe again.
	clk.Advance(5 * time.Minute)
	sweep(r, []*Instance{inst})
	if calls != 5 {
		t.Fatalf("expected second probe after doubled cooldown, got %d calls", calls)
	}
}

// TestReviveBreaker_HealthyReset verifies ClassAlive wipes futility state so a
// recovered session revives normally again.
func TestReviveBreaker_HealthyReset(t *testing.T) {
	b, _, _ := newTestBreaker()
	calls := 0
	pipeAlive := false
	r := &Reviver{
		TmuxExists:   func(string, string) bool { return true },
		PipeAlive:    func(string) bool { return pipeAlive },
		ReviveAction: func(*Instance) error { calls++; return nil },
		Stagger:      0,
		Breaker:      b,
	}
	inst := newReviverTestInstance("flapper", StatusError)

	// Trip the circuit.
	for i := 0; i < 4; i++ {
		sweep(r, []*Instance{inst})
	}
	if b.openCircuits() != 1 {
		t.Fatalf("setup: expected open circuit")
	}

	// Session recovers: pipe alive AND status cleared → ClassAlive.
	pipeAlive = true
	inst.Status = StatusRunning
	sweep(r, []*Instance{inst})
	if b.openCircuits() != 0 {
		t.Fatalf("expected circuit reset after ClassAlive")
	}

	// Regress again: fresh counting, first sweep attempts immediately.
	pipeAlive = false
	inst.Status = StatusError
	callsBefore := calls
	sweep(r, []*Instance{inst})
	if calls != callsBefore+1 {
		t.Fatalf("expected immediate attempt after reset, got %d (was %d)", calls, callsBefore)
	}
}

// TestReviveBreaker_FailedActionCountsFutile verifies a revive action that
// returns an error trips the circuit without needing the next-sweep transition.
func TestReviveBreaker_FailedActionCountsFutile(t *testing.T) {
	b, _, _ := newTestBreaker()
	calls := 0
	r := &Reviver{
		TmuxExists:   func(string, string) bool { return true },
		PipeAlive:    func(string) bool { return false },
		ReviveAction: func(*Instance) error { calls++; return errTest },
		Stagger:      0,
		Breaker:      b,
	}
	inst := newReviverTestInstance("failer", StatusError)

	// Each failed action is immediately futile → 3 sweeps trip it.
	for i := 0; i < 3; i++ {
		sweep(r, []*Instance{inst})
	}
	if b.openCircuits() != 1 {
		t.Fatalf("expected circuit open after 3 failed actions, got %d open", b.openCircuits())
	}
	// Next sweep skips.
	callsBefore := calls
	outs := sweep(r, []*Instance{inst})
	if calls != callsBefore {
		t.Fatalf("expected skip after trip, got extra calls")
	}
	if !outs[0].CircuitOpen {
		t.Errorf("expected CircuitOpen outcome")
	}
}

// TestReviveBreaker_WedgeWarnOnceRateLimited verifies the wedge warning fires
// when ≥2 circuits open and is rate-limited to once per WedgeWarnInterval.
func TestReviveBreaker_WedgeWarnOnceRateLimited(t *testing.T) {
	b, clk, h := newTestBreaker()
	calls := 0
	r := wedgedReviver(b, &calls)
	a := newReviverTestInstance("a", StatusError)
	c := newReviverTestInstance("c", StatusError)

	// Trip both circuits.
	for i := 0; i < 5; i++ {
		sweep(r, []*Instance{a, c})
	}
	if b.openCircuits() != 2 {
		t.Fatalf("expected 2 open circuits, got %d", b.openCircuits())
	}
	if got := h.count("reviver_tmux_wedge_suspected"); got != 1 {
		t.Fatalf("expected exactly 1 wedge warning, got %d", got)
	}

	// More sweeps within the interval must not re-warn.
	for i := 0; i < 3; i++ {
		sweep(r, []*Instance{a, c})
	}
	if got := h.count("reviver_tmux_wedge_suspected"); got != 1 {
		t.Fatalf("expected wedge warning rate-limited to 1, got %d", got)
	}

	// After the interval, a fresh open-circuit event may warn again.
	clk.Advance(WedgeWarnInterval + time.Minute)
	// Force a re-futile probe on both to re-enter recordFutile with open circuits.
	clk.Advance(MaxCooldown)    // ensure cooldown expired so probes fire
	sweep(r, []*Instance{a, c}) // probes
	sweep(r, []*Instance{a, c}) // futile → recordFutile → warn again
	if got := h.count("reviver_tmux_wedge_suspected"); got < 2 {
		t.Fatalf("expected wedge warning to re-fire after interval, got %d", got)
	}
}

// TestReviveBreaker_NilBreakerLegacy verifies a Reviver with no breaker keeps
// the exact pre-#1579 storm behavior (unbounded respawns, no CircuitOpen).
func TestReviveBreaker_NilBreakerLegacy(t *testing.T) {
	calls := 0
	r := wedgedReviver(nil, &calls) // Breaker set to nil below
	r.Breaker = nil
	inst := newReviverTestInstance("legacy", StatusError)

	for i := 0; i < 10; i++ {
		outs := sweep(r, []*Instance{inst})
		if outs[0].CircuitOpen {
			t.Fatalf("nil breaker must never report CircuitOpen")
		}
	}
	if calls != 10 {
		t.Fatalf("nil breaker must respawn every sweep (10), got %d", calls)
	}
}

// TestReviveBreaker_Prune verifies stale entries are dropped after EntryTTL.
func TestReviveBreaker_Prune(t *testing.T) {
	b, clk, _ := newTestBreaker()
	calls := 0
	r := wedgedReviver(b, &calls)
	inst := newReviverTestInstance("stale", StatusError)

	sweep(r, []*Instance{inst})
	b.mu.Lock()
	n := len(b.entries)
	b.mu.Unlock()
	if n != 1 {
		t.Fatalf("expected 1 tracked entry, got %d", n)
	}

	// Advance beyond TTL and prune (ReviveAll calls Prune with an empty list).
	clk.Advance(EntryTTL + time.Minute)
	sweep(r, nil)
	b.mu.Lock()
	n = len(b.entries)
	b.mu.Unlock()
	if n != 0 {
		t.Fatalf("expected stale entry pruned, got %d", n)
	}
}

// TestIssue1579_StormBounded is the before/after pin: on a wedged server the
// breaker caps respawns while the legacy path storms unboundedly.
func TestIssue1579_StormBounded(t *testing.T) {
	const sweeps = 40 // the issue observed 37 respawns in ~9 minutes

	// Legacy (no breaker): one respawn per sweep, unbounded.
	legacyCalls := 0
	legacy := wedgedReviver(nil, &legacyCalls)
	legacy.Breaker = nil
	inst := newReviverTestInstance("victim", StatusError)
	for i := 0; i < sweeps; i++ {
		sweep(legacy, []*Instance{inst})
	}
	if legacyCalls != sweeps {
		t.Fatalf("legacy should storm %d×, got %d", sweeps, legacyCalls)
	}

	// With breaker: bounded far below the sweep count.
	b, _, _ := newTestBreaker()
	brokenCalls := 0
	broken := wedgedReviver(b, &brokenCalls)
	inst2 := newReviverTestInstance("victim2", StatusError)
	for i := 0; i < sweeps; i++ {
		sweep(broken, []*Instance{inst2})
	}
	if brokenCalls > FutilityThreshold {
		t.Fatalf("breaker should bound respawns to ≤%d before opening, got %d", FutilityThreshold, brokenCalls)
	}
	if brokenCalls >= legacyCalls {
		t.Fatalf("breaker (%d) must respawn far fewer than legacy (%d)", brokenCalls, legacyCalls)
	}
}
