package tmux

import (
	"sort"
	"sync"
	"testing"
	"time"
)

// TestTriggerCloseGated_SerializesWithStagger pins the closeGate primitive's
// behavior: N concurrent triggerCloseGated calls must record their trigger
// times >= closeGateStagger apart (modulo a small scheduler-jitter slack).
// The gate's purpose is to keep server-side detach triggers from stacking
// inside tmux's notify-walk drain window (the freed-but-still-listed race
// in tmux/tmux#4980).
//
// This is a unit test for the gating primitive only; the integration shape
// (gated burst doesn't crash a real tmux server) is exercised by the
// AGENT_DECK_BURST_TEST=1 burst tests.
func TestTriggerCloseGated_SerializesWithStagger(t *testing.T) {
	const n = 8
	times := make([]time.Time, n)

	var wg sync.WaitGroup
	barrier := make(chan struct{})
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-barrier
			triggerCloseGated(func() { times[i] = time.Now() })
		}()
	}
	close(barrier)
	wg.Wait()

	sort.Slice(times, func(a, b int) bool { return times[a].Before(times[b]) })

	// Allow 5 ms slack for goroutine scheduling jitter — empirically
	// time.Sleep on macOS overshoots by 1-3 ms but rarely undershoots.
	tolerance := 5 * time.Millisecond
	want := closeGateStagger - tolerance
	for i := 1; i < n; i++ {
		gap := times[i].Sub(times[i-1])
		if gap < want {
			t.Errorf("gap[%d] = %v < %v (closeGateStagger=%v - %v slack)",
				i, gap, want, closeGateStagger, tolerance)
		}
	}
}
