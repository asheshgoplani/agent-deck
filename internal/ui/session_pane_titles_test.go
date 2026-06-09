package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Session row pane-title suffix. The dim task-description suffix (the tmux
// pane title) appears on every row when [display] show_pane_titles = true,
// not just the selected one. With the flag off it stays selected-only — the
// original hardcoded behavior. The flag is cached on Home at startup and
// refreshed when the settings panel saves.
//
// renderRowWithPaneTitle renders one session row with the given selection and
// show_pane_titles flag and returns the rendered string for assertions.
func renderRowWithPaneTitle(t *testing.T, selected, showAll bool, paneTitle string) string {
	t.Helper()
	forceTrueColorProfile()

	h := &Home{width: 140, showPaneTitles: showAll}
	inst := &session.Instance{
		ID:    "sess-pane-title",
		Title: "with-pane-title",
	}
	item := session.Item{
		Type:          session.ItemTypeSession,
		Session:       inst,
		Level:         1,
		Path:          "test",
		IsLastInGroup: true,
	}
	snapshot := map[string]sessionRenderState{
		inst.ID: {
			status:    session.StatusRunning,
			tool:      "claude",
			paneTitle: paneTitle,
		},
	}

	var b strings.Builder
	h.renderSessionItem(&b, item, selected, snapshot, h.width)
	return b.String()
}

const sampleTaskTitle = "Explore messaging support features"

// TestPaneTitle_ShowAllRendersOnUnselectedRow verifies show_pane_titles=true
// renders the pane-title suffix on an unselected row.
func TestPaneTitle_ShowAllRendersOnUnselectedRow(t *testing.T) {
	row := renderRowWithPaneTitle(t, false, true, sampleTaskTitle)
	if !strings.Contains(row, sampleTaskTitle) {
		t.Fatalf("show_pane_titles=true must render the pane-title suffix on an unselected row, "+
			"but %q was not found. Got: %q", sampleTaskTitle, row)
	}
}

// TestPaneTitle_ShowAllOffOmitsOnUnselectedRow verifies show_pane_titles=false
// keeps the pane-title suffix off an unselected row.
func TestPaneTitle_ShowAllOffOmitsOnUnselectedRow(t *testing.T) {
	row := renderRowWithPaneTitle(t, false, false, sampleTaskTitle)
	if strings.Contains(row, sampleTaskTitle) {
		t.Fatalf("show_pane_titles=false must not render the pane-title suffix on an unselected row, "+
			"but %q leaked into the default path. Got: %q", sampleTaskTitle, row)
	}
}

// TestPaneTitle_SelectedRowAlwaysRenders verifies the selected row renders its
// pane-title suffix even when show_pane_titles is off.
func TestPaneTitle_SelectedRowAlwaysRenders(t *testing.T) {
	// Selected-only behavior must be preserved when the toggle is off.
	row := renderRowWithPaneTitle(t, true, false, sampleTaskTitle)
	if !strings.Contains(row, sampleTaskTitle) {
		t.Fatalf("the selected row must always render its pane-title suffix regardless of "+
			"show_pane_titles, but %q was not found. Got: %q", sampleTaskTitle, row)
	}
}
