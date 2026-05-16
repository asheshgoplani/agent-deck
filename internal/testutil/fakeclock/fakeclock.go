// Package fakeclock provides an injectable Clock for tests that exercise
// time-sensitive logic — hook freshness windows, heartbeat staleness,
// log-rotation maintenance — without sleeping or depending on wall-clock
// progress.
//
// Per TUI-TEST-PLAN.md §6.3 (fakeClock): production call sites that
// currently invoke time.Now() directly (e.g. instance.go's hook fast-path
// window) must be refactored to take a Clock so they can be driven by Fake
// in tests. Real{} is the production wiring.
//
// Usage:
//
//	c := fakeclock.New(time.Unix(0, 0))
//	inst.UpdateStatus("running", c.Now())
//	c.Advance(2 * time.Second)
//	inst.UpdateStatus("idle", c.Now()) // 2s later from the model's POV
package fakeclock

import (
	"sync"
	"time"
)

// Clock is the seam production code should depend on instead of time.Now().
type Clock interface {
	Now() time.Time
}

// Real is the wall-clock implementation. Use this in production wiring.
type Real struct{}

// Now returns time.Now().
func (Real) Now() time.Time { return time.Now() }

// Fake is a controllable clock. Concurrent Now / Advance / Set are safe.
type Fake struct {
	mu  sync.Mutex
	now time.Time
}

// New returns a Fake seeded at the given time.
func New(seed time.Time) *Fake {
	return &Fake{now: seed}
}

// Now returns the current fake time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Advance moves the clock forward by d. Negative durations are ignored —
// time only moves forward, matching the contract production code expects
// from time.Now() between successive calls.
func (f *Fake) Advance(d time.Duration) {
	if d <= 0 {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// Set jumps the clock to t (forward or backward). Tests that need to
// simulate a specific wall-clock instant (e.g. a known timestamp parsed
// out of a hook payload) should use Set; otherwise prefer Advance.
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t
}
