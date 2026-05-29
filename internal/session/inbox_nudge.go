package session

import (
	"sync"
	"time"
)

// Issue #1225 Tier-2 wake-nudge. Delivery is PULL (the parent drains its
// durable outbox on Stop/heartbeat); this nudge is a pure latency optimization
// that wakes an IDLE parent sooner than its heartbeat by firing one debounced
// `send-keys` into its pane. It is gated on idle because a send-keys into a
// busy pane only queues the keystroke (issue #36326) — the exact failure the
// old push model died on. Correctness never depends on the nudge landing: a
// dropped or suppressed nudge just means the record waits for the next
// heartbeat/turn boundary.
//
// The OS-level trigger (an inotify IN_CLOSE_WRITE watcher on the inboxes dir
// that calls Nudge) is daemon glue flagged for maintainer wiring; this type is
// the testable, platform-independent policy core.

// WakeNudger debounces per-parent wake nudges.
type WakeNudger struct {
	debounce time.Duration
	mu       sync.Mutex
	last     map[string]time.Time
}

// NewWakeNudger returns a nudger that suppresses repeat nudges to the same
// parent within debounce of the previous one.
func NewWakeNudger(debounce time.Duration) *WakeNudger {
	return &WakeNudger{debounce: debounce, last: map[string]time.Time{}}
}

// Nudge fires one wake into parentID's pane via send IFF isIdle reports the
// pane idle AND the debounce window since the last nudge has elapsed. Returns
// whether a send was attempted, plus send's error (best-effort — callers may
// ignore it; a failed wake is harmless). now is injected for deterministic
// tests; production passes time.Now().
func (w *WakeNudger) Nudge(parentID string, now time.Time, isIdle func() bool, send func() error) (bool, error) {
	if isIdle == nil || !isIdle() {
		return false, nil // never nudge a busy pane
	}
	w.mu.Lock()
	if last, ok := w.last[parentID]; ok && w.debounce > 0 && now.Sub(last) < w.debounce {
		w.mu.Unlock()
		return false, nil // within debounce window
	}
	w.last[parentID] = now
	w.mu.Unlock()

	if send == nil {
		return true, nil
	}
	return true, send()
}
