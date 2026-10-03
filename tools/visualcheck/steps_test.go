package main

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestCycleRestorePressesReturnsToDefault pins the restore presses in
// stepStateVisibility to the real mode counts. A hard-coded count went stale
// when the time filter gained a fifth mode, leaving "Last 30 days" engaged
// and leaking its legend into every later golden frame.
func TestCycleRestorePressesReturnsToDefault(t *testing.T) {
	for _, c := range []struct {
		key   string
		modes int
	}{
		{"*", session.TimeFilterModeCount},
		{"t", session.GroupViewModeCount},
	} {
		// One press captures the frame; the restore presses must complete
		// exactly one full cycle back to mode 0, the default.
		got := cycleRestorePresses(c.key)
		if mode := (1 + got) % c.modes; mode != 0 || got >= c.modes {
			t.Errorf("key %q: %d restore presses over %d modes ends on mode %d, want %d presses back to the default",
				c.key, got, c.modes, mode, c.modes-1)
		}
	}
}
