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

// TestShellPromptReady accepts the sandbox shell's prompt whether it runs as
// a regular user ($) or as root (#), so the attach step works in a root
// container. Only the last non-empty line counts.
func TestShellPromptReady(t *testing.T) {
	for _, c := range []struct {
		pane string
		want bool
	}{
		{"host:shell-live user$ \n\n", true},
		{"host:shell-live root# \n", true},
		{"$ ", true},
		{"# ", true},
		{"$ clear\n", false},
		{"", false},
		{"\n\n", false},
	} {
		if got := shellPromptReady(c.pane); got != c.want {
			t.Errorf("shellPromptReady(%q) = %v, want %v", c.pane, got, c.want)
		}
	}
}
