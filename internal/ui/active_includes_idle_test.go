package ui

// Issue #2452 wiring: [ui] active_includes_idle reaches Home.rebuildFlatItems
// and the rendered list. With it on, an idle session with a live pane stays
// above the divider and a stopped one sinks; with it off the active-on-top
// view renders exactly as before.

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func setStatuses(t *testing.T, h *Home, st map[string]session.Status) {
	t.Helper()
	h.instancesMu.Lock()
	defer h.instancesMu.Unlock()
	for _, inst := range h.instances {
		if s, ok := st[inst.Title]; ok {
			inst.Status = s
		}
	}
}

var juggledStatuses = map[string]session.Status{
	"a1": session.StatusRunning,
	"a2": session.StatusIdle, // live pane, just went idle
	"a3": session.StatusStopped,
	"b1": session.StatusIdle,
	"b2": session.StatusError,
}

func TestActiveIncludesIdle_WiringKeepsIdleOnTop(t *testing.T) {
	home, _ := buildTwoGroupHome(t)
	setStatuses(t, home, juggledStatuses)
	home.viewModeOpts = session.ViewModeOptions{ActiveIncludesIdle: true}
	home.groupViewMode = session.GroupViewActiveTop
	home.rebuildFlatItems()

	div := dividerIndex(home)
	if div < 0 {
		t.Fatal("expected a divider: live and stopped sessions coexist")
	}
	for _, title := range []string{"a1", "a2", "b1"} {
		if idx := sessionIndexByTitle(home, title); idx < 0 || idx > div {
			t.Fatalf("%q (live pane) must be above the divider: idx=%d divider=%d", title, idx, div)
		}
	}
	for _, title := range []string{"a3", "b2"} {
		if idx := sessionIndexByTitle(home, title); idx < div {
			t.Fatalf("%q (no live pane) must sink below the divider: idx=%d divider=%d", title, idx, div)
		}
	}
	view := stripAnsi(home.renderSessionList(120, 30))
	if !strings.Contains(view, "stopped / done") || !strings.Contains(view, "alpha (stopped)") {
		t.Fatalf("bottom section must be labelled stopped, not idle:\n%s", view)
	}
	if strings.Contains(view, "(idle)") || strings.Contains(view, "idle / done") {
		t.Fatalf("idle label shown although idle sessions are on top:\n%s", view)
	}
}

// Option off (the default): idle sessions sink and the rendered list is
// byte-identical to a Home that never had the option set.
func TestActiveIncludesIdle_OffRendersUnchanged(t *testing.T) {
	render := func(opts *session.ViewModeOptions) (*Home, string) {
		home, _ := buildTwoGroupHome(t)
		setStatuses(t, home, juggledStatuses)
		if opts != nil {
			home.viewModeOpts = *opts
		}
		home.groupViewMode = session.GroupViewActiveTop
		home.rebuildFlatItems()
		return home, home.renderSessionList(120, 30)
	}
	home, explicitOff := render(&session.ViewModeOptions{ActiveIncludesIdle: false})
	_, untouched := render(nil)
	if explicitOff != untouched {
		t.Fatalf("explicit active_includes_idle=false changed the rendering:\n%s\n---\n%s", explicitOff, untouched)
	}
	div := dividerIndex(home)
	if a1 := sessionIndexByTitle(home, "a1"); a1 < 0 || a1 > div {
		t.Fatalf("running a1 must be above the divider: idx=%d divider=%d", a1, div)
	}
	for _, title := range []string{"a2", "b1", "a3", "b2"} {
		if idx := sessionIndexByTitle(home, title); idx < div {
			t.Fatalf("%q must be below the divider with the option off: idx=%d divider=%d", title, idx, div)
		}
	}
	view := stripAnsi(untouched)
	if !strings.Contains(view, "idle / done") || !strings.Contains(view, "alpha (idle)") {
		t.Fatalf("default labels changed:\n%s", view)
	}
}
