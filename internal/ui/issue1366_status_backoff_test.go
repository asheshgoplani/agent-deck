package ui

import (
	"testing"
	"time"
)

// Issue #1366: the status worker ticks on a FIXED 2s interval with no
// protection against a sweep that overruns the interval. When tmux is under
// load (e.g. no/degraded control-mode pipe at ~100 sessions) a sweep can take
// longer than 2s, so sweeps pile up and pin the tmux server. nextStatusInterval
// keeps the base cadence when sweeps are fast and backs off (bounding the duty
// cycle to ~50%) when a sweep overruns, capped at max.
func TestNextStatusInterval(t *testing.T) {
	base := 2 * time.Second
	max := 10 * time.Second
	cases := []struct {
		name      string
		lastSweep time.Duration
		want      time.Duration
	}{
		{"fast sweep keeps base", 100 * time.Millisecond, base},
		{"just under base keeps base", 1900 * time.Millisecond, base},
		{"equal to base keeps base", base, base},
		{"overrun backs off to 2x", 3 * time.Second, 6 * time.Second},
		{"large overrun caps at max", 20 * time.Second, max},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nextStatusInterval(tc.lastSweep, base, max); got != tc.want {
				t.Errorf("nextStatusInterval(%v) = %v, want %v", tc.lastSweep, got, tc.want)
			}
		})
	}
}
