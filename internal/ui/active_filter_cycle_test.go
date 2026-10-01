package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// With active_filter_excludes = ["error"] (stopped kept visible), % cycles
// All -> Open -> Open+stopped hidden -> All, so hiding stopped sessions is one
// keypress away without editing config. When the exclude set already hides
// stopped (the default), the extra step would be a no-op and % stays a toggle.
func TestActiveFilterCycle_AddsHideStoppedStep(t *testing.T) {
	press := func(h *Home) (session.Status, bool) {
		h.changeStatusFilter(FilterModeActive)
		return h.statusFilter, h.matchesStatusFilter(h.statusFilter, session.StatusStopped)
	}

	h := NewHome()
	h.activeFilterExcludes = map[session.Status]bool{session.StatusError: true}
	for i, want := range []struct {
		filter       session.Status
		stoppedShown bool
	}{
		{FilterModeActive, true},  // Open: config excludes only error
		{FilterModeActive, false}, // Open + stopped hidden
		{"", true},                // back to All
		{FilterModeActive, true},  // cycle restarts at the configured step
	} {
		filter, shown := press(h)
		if filter == "" {
			shown = true // All shows everything; matchesStatusFilter is not consulted
		}
		if filter != want.filter || shown != want.stoppedShown {
			t.Fatalf("press %d: filter=%q stoppedShown=%v, want %q/%v", i+1, filter, shown, want.filter, want.stoppedShown)
		}
	}

	d := NewHome()
	d.activeFilterExcludes = map[session.Status]bool{session.StatusError: true, session.StatusStopped: true}
	if f, _ := press(d); f != FilterModeActive {
		t.Fatalf("default press 1: filter=%q, want active", f)
	}
	if f, _ := press(d); f != "" {
		t.Fatalf("default press 2: filter=%q, want All (two-step toggle)", f)
	}
}
