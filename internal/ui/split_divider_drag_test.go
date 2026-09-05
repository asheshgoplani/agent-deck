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
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func dragTestItems() []session.Item {
	return []session.Item{
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s1", Title: "S1"}, Level: 0},
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s2", Title: "S2"}, Level: 0},
	}
}

func newDividerTestHome() *Home {
	return newTestHomeWithItems(100, 30, dragTestItems())
}

func TestIsOnDivider_MatchesSeparatorColumns(t *testing.T) {
	setIsolatedAgentDeckDir(t)
	// width 100, preview_pct 65 -> sessions pane width 35.
	// The " │ " separator occupies columns [35, 38).
	h := newDividerTestHome()

	if got := h.sessionsPaneWidth(); got != 35 {
		t.Fatalf("precondition: sessionsPaneWidth = %d, want 35", got)
	}

	// The drawn separator is columns [35, 38); dividerGrabSlop widens the grab
	// target by 2 columns each way to [33, 40) so the handle is actually
	// hittable with a mouse.
	cases := []struct {
		x    int
		want bool
	}{
		{32, false}, // outside the slop, plain sessions column
		{33, true},  // slop, left
		{34, true},  // slop, left
		{35, true},  // separator left space
		{36, true},  // the │ glyph
		{37, true},  // separator right space
		{38, true},  // slop, right
		{39, true},  // slop, right
		{40, false}, // outside the slop, plain preview column
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

// The "click and release stays dragging" bug. Under all-motion reporting a
// terminal that is moving the mouse when the button comes up encodes the
// release with the motion bit set, and Bubble Tea's parser will not promote a
// motion report to a release — so the drag never hears about it. The give-away
// is the next event: an unheld cursor reports motion with MouseButtonNone,
// where a real drag keeps reporting MouseButtonLeft.
func TestDividerDrag_LostRelease_UnsticksOnNextButtonlessMotion(t *testing.T) {
	setIsolatedAgentDeckDir(t)
	h := newDividerTestHome()

	model, _ := h.Update(tea.MouseMsg{X: 36, Y: 5, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	h = model.(*Home)
	if !h.draggingDivider {
		t.Fatalf("press on divider did not start a drag")
	}

	// The release arrives disguised as one more left-drag step (this is the
	// event the terminal actually sent), so the drag legitimately continues.
	model, _ = h.Update(tea.MouseMsg{X: 20, Y: 5, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	h = model.(*Home)
	if !h.draggingDivider {
		t.Fatalf("left-button motion should still be treated as a drag")
	}
	if h.getPreviewPct() != 80 {
		t.Fatalf("drag to x=20: previewPct = %d, want 80", h.getPreviewPct())
	}

	// Now the user just moves the mouse with nothing held. Before the fix the
	// divider chased the cursor forever; now it lets go, keeping the ratio the
	// drag ended at.
	model, _ = h.Update(tea.MouseMsg{X: 70, Y: 5, Button: tea.MouseButtonNone, Action: tea.MouseActionMotion})
	h = model.(*Home)
	if h.draggingDivider {
		t.Fatalf("button-less motion did not release the stuck drag")
	}
	if h.getPreviewPct() != 80 {
		t.Fatalf("unstick moved the split to %d, want it left at 80", h.getPreviewPct())
	}

	session.ClearUserConfigCache()
	cfg, err := session.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if cfg.UI.PreviewPct != 80 {
		t.Fatalf("unstick persisted preview_pct = %d, want 80", cfg.UI.PreviewPct)
	}
}

// A second press while the first drag is still "held" means the release was
// lost. The old handler swallowed that press and stayed stuck; now it closes
// the stale drag and starts a clean one.
func TestDividerDrag_PressWhileStuck_StartsCleanDrag(t *testing.T) {
	setIsolatedAgentDeckDir(t)
	h := newDividerTestHome()

	model, _ := h.Update(tea.MouseMsg{X: 36, Y: 5, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	h = model.(*Home)
	model, _ = h.Update(tea.MouseMsg{X: 36, Y: 5, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	h = model.(*Home)
	if !h.draggingDivider {
		t.Fatalf("re-press on the divider should leave a live drag")
	}

	// A re-press away from the divider ends the drag instead of re-arming it.
	model, _ = h.Update(tea.MouseMsg{X: 36, Y: 5, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	h = model.(*Home)
	model, _ = h.Update(tea.MouseMsg{X: 10, Y: 5, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	h = model.(*Home)
	if h.draggingDivider {
		t.Fatalf("press off the divider should not keep the drag alive")
	}
}

// Hovering the grab zone arms the handle visually, so you can tell it is
// draggable without discovering it by trial and error.
func TestDividerHover_TracksGrabZoneAndThickensSeparator(t *testing.T) {
	setIsolatedAgentDeckDir(t)
	h := newDividerTestHome()

	model, _ := h.Update(tea.MouseMsg{X: 10, Y: 5, Button: tea.MouseButtonNone, Action: tea.MouseActionMotion})
	h = model.(*Home)
	if h.hoverDivider {
		t.Fatalf("hover over the list marked the divider hovered")
	}
	if strings.Contains(ansi.Strip(h.View()), "┃") {
		t.Fatalf("unhovered divider drew the thick glyph")
	}

	model, _ = h.Update(tea.MouseMsg{X: 34, Y: 5, Button: tea.MouseButtonNone, Action: tea.MouseActionMotion})
	h = model.(*Home)
	if !h.hoverDivider {
		t.Fatalf("hover inside the grab slop did not mark the divider hovered")
	}
	if !strings.Contains(ansi.Strip(h.View()), "┃") {
		t.Fatalf("hovered divider did not thicken:\n%s", ansi.Strip(h.View()))
	}

	model, _ = h.Update(tea.MouseMsg{X: 60, Y: 5, Button: tea.MouseButtonNone, Action: tea.MouseActionMotion})
	h = model.(*Home)
	if h.hoverDivider {
		t.Fatalf("hover moved off the divider but stayed marked")
	}
}

// Hover is derived state with one input: the last known mouse position. Every
// path that stops delivering that input has to drop it, or the divider stays
// lit under a mouse that moved away — and on terminals that honor OSC 22, the
// grab cursor stays with it.
func TestDividerHover_ClearsWhenTheMouseStopsReporting(t *testing.T) {
	hover := func(t *testing.T) *Home {
		t.Helper()
		h := newDividerTestHome()
		model, _ := h.Update(tea.MouseMsg{X: 36, Y: 5, Button: tea.MouseButtonNone, Action: tea.MouseActionMotion})
		h = model.(*Home)
		if !h.hoverDivider {
			t.Fatalf("precondition: motion over the divider did not set hover")
		}
		return h
	}

	t.Run("wheel off the divider", func(t *testing.T) {
		setIsolatedAgentDeckDir(t)
		h := hover(t)
		// Wheel events are routed to the scrollable area and never reach the
		// drag handler, so hover has to be recomputed before that split.
		model, _ := h.Update(tea.MouseMsg{X: 10, Y: 5, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
		if model.(*Home).hoverDivider {
			t.Fatal("scrolling away from the divider left it hovered")
		}
	})

	t.Run("keystroke", func(t *testing.T) {
		setIsolatedAgentDeckDir(t)
		h := hover(t)
		model, _ := h.Update(tea.KeyMsg{Type: tea.KeyDown})
		if model.(*Home).hoverDivider {
			t.Fatal("keyboard input left the divider hovered")
		}
	})

	t.Run("window blur", func(t *testing.T) {
		setIsolatedAgentDeckDir(t)
		h := hover(t)
		model, _ := h.Update(tea.BlurMsg{})
		if model.(*Home).hoverDivider {
			t.Fatal("losing window focus left the divider hovered")
		}
	})

	t.Run("resize", func(t *testing.T) {
		setIsolatedAgentDeckDir(t)
		h := hover(t)
		model, _ := h.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
		if model.(*Home).hoverDivider {
			t.Fatal("resizing left the divider hovered against the old geometry")
		}
	})

	t.Run("modal opens", func(t *testing.T) {
		setIsolatedAgentDeckDir(t)
		h := hover(t)
		h.helpOverlay.Show()
		model, _ := h.Update(tea.MouseMsg{X: 36, Y: 5, Button: tea.MouseButtonNone, Action: tea.MouseActionMotion})
		if model.(*Home).hoverDivider {
			t.Fatal("a modal over the divider left it hovered")
		}
	})
}

// The backstop for channels we cannot observe — chiefly embedded mode, where
// the input router forwards events inside the session pane straight to tmux, so
// moving off the divider into the terminal beside it delivers nothing here.
func TestDividerHover_ExpiresWithoutConfirmation(t *testing.T) {
	setIsolatedAgentDeckDir(t)
	h := newDividerTestHome()

	model, _ := h.Update(tea.MouseMsg{X: 36, Y: 5, Button: tea.MouseButtonNone, Action: tea.MouseActionMotion})
	h = model.(*Home)
	if !h.hoverDivider {
		t.Fatal("precondition: motion over the divider did not set hover")
	}

	// A pause shorter than the TTL must not cost the highlight: approaching the
	// divider and hesitating before the click is the normal way to use it.
	h.expireDividerHover(h.hoverDividerAt.Add(hoverDividerTTL - time.Millisecond))
	if !h.hoverDivider {
		t.Fatal("hover expired during an ordinary pause before clicking")
	}

	h.expireDividerHover(h.hoverDividerAt.Add(hoverDividerTTL + time.Millisecond))
	if h.hoverDivider {
		t.Fatal("hover survived past its TTL with no mouse event confirming it")
	}
}

// Expiry is cosmetic only: the press recomputes hover before the grab check, so
// a highlight that timed out while the mouse sat still still grabs on click.
func TestDividerHover_ExpiredHighlightStillGrabsOnPress(t *testing.T) {
	setIsolatedAgentDeckDir(t)
	h := newDividerTestHome()

	model, _ := h.Update(tea.MouseMsg{X: 36, Y: 5, Button: tea.MouseButtonNone, Action: tea.MouseActionMotion})
	h = model.(*Home)
	h.expireDividerHover(h.hoverDividerAt.Add(2 * hoverDividerTTL))
	if h.hoverDivider {
		t.Fatal("precondition: hover should have expired")
	}

	model, _ = h.Update(tea.MouseMsg{X: 36, Y: 5, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if !model.(*Home).draggingDivider {
		t.Fatal("press on the divider after hover expiry did not start a drag")
	}
}

// Resizing requires a detached dashboard. While a session is attached the input
// router forwards mouse events inside the session pane straight to tmux, so the
// dashboard would see the pointer reach the divider and never hear it leave —
// an unfixable stale highlight. The divider is inert instead.
func TestDividerResize_InertWhileAttached(t *testing.T) {
	setIsolatedAgentDeckDir(t)
	h := newDividerTestHome()

	model, _ := h.Update(tea.MouseMsg{X: 36, Y: 5, Button: tea.MouseButtonNone, Action: tea.MouseActionMotion})
	h = model.(*Home)
	if !h.hoverDivider || !h.dividerResizeEnabled() {
		t.Fatal("precondition: detached dashboard should hover and allow resize")
	}

	h.embeddedMode = true
	if h.dividerResizeEnabled() {
		t.Fatal("attached dashboard still reports the divider resizable")
	}

	// Hover cannot be established while attached...
	model, _ = h.Update(tea.MouseMsg{X: 36, Y: 5, Button: tea.MouseButtonNone, Action: tea.MouseActionMotion})
	h = model.(*Home)
	if h.hoverDivider {
		t.Fatal("attached dashboard hovered the divider")
	}
	if strings.Contains(ansi.Strip(h.View()), "┃") {
		t.Fatal("attached dashboard drew the hover glyph")
	}

	// ...and neither can a drag.
	model, _ = h.Update(tea.MouseMsg{X: 36, Y: 5, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	h = model.(*Home)
	if h.draggingDivider {
		t.Fatal("press on the divider started a drag while attached")
	}

	// Detaching brings it back.
	h.embeddedMode = false
	model, _ = h.Update(tea.MouseMsg{X: 36, Y: 5, Button: tea.MouseButtonNone, Action: tea.MouseActionMotion})
	if !model.(*Home).hoverDivider {
		t.Fatal("detaching did not restore the divider")
	}
}

// A drag already in flight when a session is attached ends rather than
// following a mouse the dashboard can no longer see.
func TestDividerDrag_EndsWhenTheDividerGoesInert(t *testing.T) {
	setIsolatedAgentDeckDir(t)
	h := newDividerTestHome()

	model, _ := h.Update(tea.MouseMsg{X: 36, Y: 5, Button: tea.MouseButtonNone, Action: tea.MouseActionMotion})
	h = model.(*Home)
	model, _ = h.Update(tea.MouseMsg{X: 36, Y: 5, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	h = model.(*Home)
	if !h.draggingDivider {
		t.Fatal("precondition: press did not start a drag")
	}

	h.embeddedMode = true
	model, _ = h.Update(tea.MouseMsg{X: 20, Y: 5, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	if model.(*Home).draggingDivider {
		t.Fatal("drag survived the divider going inert")
	}
}

// The reset shape has to be a name the terminal recognizes. Ghostty ignores an
// OSC 22 payload that is not a CSS cursor name, so an empty reset leaves the
// grab cursor on screen with nothing in the code looking wrong.
func TestPointerShapeResetUsesANamedShape(t *testing.T) {
	if pointerShapeDefault != "default" {
		t.Fatalf("pointerShapeDefault = %q, want the CSS name \"default\"", pointerShapeDefault)
	}
}
