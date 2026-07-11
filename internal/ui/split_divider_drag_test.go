// Mouse-draggable Sessions/Preview divider.
//
// The dual layout draws a " │ " separator between the SESSIONS list and the
// PREVIEW pane. Grabbing that separator with the left mouse button and dragging
// resizes the split live; releasing persists the new ratio to config.toml.
// This complements the existing < / > keybindings (issue #1092).
//
// RemoteSession note: divider dragging is a layout-level interaction that does
// not branch on item types — the grab test and the mouse-column → preview_pct
// math are purely geometric — so RemoteSession coverage is not applicable here
// (no t.Skip needed; the code path is item-type-agnostic).

package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func dragTestItems() []session.Item {
	return []session.Item{
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s1", Title: "S1"}, Level: 0},
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s2", Title: "S2"}, Level: 0},
	}
}

func TestIsOnDivider_MatchesSeparatorColumns(t *testing.T) {
	setIsolatedAgentDeckDir(t)
	// width 100, preview_pct 65 -> sessions pane width 35.
	// The " │ " separator occupies columns [35, 38).
	h := newTestHomeWithItems(100, 30, dragTestItems())

	if got := h.sessionsPaneWidth(); got != 35 {
		t.Fatalf("precondition: sessionsPaneWidth = %d, want 35", got)
	}

	cases := []struct {
		x    int
		want bool
	}{
		{34, false}, // last sessions column
		{35, true},  // separator left space
		{36, true},  // the │ glyph
		{37, true},  // separator right space
		{38, false}, // first preview column
	}
	for _, tc := range cases {
		if got := h.isOnDivider(tc.x); got != tc.want {
			t.Errorf("isOnDivider(%d) = %v, want %v", tc.x, got, tc.want)
		}
	}
}

func TestSetPreviewPctFromMouseX(t *testing.T) {
	setIsolatedAgentDeckDir(t)
	h := newTestHomeWithItems(100, 30, dragTestItems())

	cases := []struct {
		name string
		x    int
		want int
	}{
		{"middle", 50, 50},        // sessions 50% -> preview 50%
		{"bias preview", 20, 80},  // sessions 20% -> preview 80%
		{"bias sessions", 70, 30}, // sessions 70% -> preview 30%
		{"clamp to max", 2, 90},   // sessions 2% -> preview 98% -> clamp 90
		{"clamp to min", 98, 10},  // sessions 98% -> preview 2% -> clamp 10
		{"clamp below zero", -5, 90},
		{"clamp above width", 200, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h.previewPct = 65
			h.setPreviewPctFromMouseX(tc.x)
			if got := h.getPreviewPct(); got != tc.want {
				t.Errorf("setPreviewPctFromMouseX(%d): previewPct = %d, want %d", tc.x, got, tc.want)
			}
		})
	}
}

func TestDividerDrag_PressMotionRelease_ResizesAndPersists(t *testing.T) {
	setIsolatedAgentDeckDir(t)
	h := newTestHomeWithItems(100, 30, dragTestItems())

	if h.getPreviewPct() != 65 {
		t.Fatalf("precondition: previewPct = %d, want 65", h.getPreviewPct())
	}

	// Press on the divider (│ at column 36) -> starts drag, ratio unchanged.
	model, _ := h.Update(tea.MouseMsg{X: 36, Y: 5, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	h = model.(*Home)
	if !h.draggingDivider {
		t.Fatalf("press on divider did not start a drag")
	}
	if h.getPreviewPct() != 65 {
		t.Fatalf("press alone changed ratio to %d, want unchanged 65", h.getPreviewPct())
	}

	// Drag left to column 20 -> sessions 20%, preview 80%.
	model, _ = h.Update(tea.MouseMsg{X: 20, Y: 5, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	h = model.(*Home)
	if h.getPreviewPct() != 80 {
		t.Fatalf("after drag to x=20: previewPct = %d, want 80", h.getPreviewPct())
	}

	// Release -> drag ends, ratio persists to config.
	model, _ = h.Update(tea.MouseMsg{X: 20, Y: 5, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
	h = model.(*Home)
	if h.draggingDivider {
		t.Fatalf("release did not end the drag")
	}

	session.ClearUserConfigCache()
	cfg, err := session.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if cfg.UI.PreviewPct != 80 {
		t.Fatalf("persisted preview_pct = %d, want 80", cfg.UI.PreviewPct)
	}
}

func TestDividerDrag_ReleaseAsMouseButtonNone_EndsDrag(t *testing.T) {
	setIsolatedAgentDeckDir(t)
	h := newTestHomeWithItems(100, 30, dragTestItems())

	model, _ := h.Update(tea.MouseMsg{X: 36, Y: 5, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	h = model.(*Home)
	if !h.draggingDivider {
		t.Fatalf("press on divider did not start a drag")
	}

	// X10 terminals report a release with MouseButtonNone. The drag must still
	// end (we key off the drag state + action, not the button).
	model, _ = h.Update(tea.MouseMsg{X: 30, Y: 5, Button: tea.MouseButtonNone, Action: tea.MouseActionRelease})
	h = model.(*Home)
	if h.draggingDivider {
		t.Fatalf("MouseButtonNone release did not end the drag")
	}
}

func TestDividerDrag_ModalMidDrag_EndsDragAndPersists(t *testing.T) {
	setIsolatedAgentDeckDir(t)
	h := newTestHomeWithItems(100, 30, dragTestItems())

	// Grab and drag to preview 80%.
	model, _ := h.Update(tea.MouseMsg{X: 36, Y: 5, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	h = model.(*Home)
	model, _ = h.Update(tea.MouseMsg{X: 20, Y: 5, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	h = model.(*Home)
	if !h.draggingDivider || h.getPreviewPct() != 80 {
		t.Fatalf("precondition: dragging=%v pct=%d, want dragging=true pct=80", h.draggingDivider, h.getPreviewPct())
	}

	// A modal appears while the button is still held. The next mouse event is
	// swallowed by the modal guard, but the drag must not stay stuck grabbed,
	// and the dragged-to ratio must be preserved (release semantics win).
	h.helpOverlay.Show()
	model, _ = h.Update(tea.MouseMsg{X: 25, Y: 5, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	h = model.(*Home)
	if h.draggingDivider {
		t.Fatalf("modal-mid-drag did not clear the drag")
	}

	session.ClearUserConfigCache()
	cfg, err := session.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if cfg.UI.PreviewPct != 80 {
		t.Fatalf("modal-mid-drag persisted preview_pct = %d, want 80", cfg.UI.PreviewPct)
	}
}

func TestDividerDrag_PressInListStillSelects(t *testing.T) {
	setIsolatedAgentDeckDir(t)
	h := newTestHomeWithItems(100, 30, dragTestItems())
	h.cursor = 0

	// Click well inside the sessions list (column 10) on the second row.
	model, _ := h.Update(tea.MouseMsg{X: 10, Y: 5, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	h = model.(*Home)

	if h.draggingDivider {
		t.Fatalf("list click erroneously started a divider drag")
	}
	if h.cursor != 1 {
		t.Fatalf("list click did not select row 1 (cursor = %d)", h.cursor)
	}
}
