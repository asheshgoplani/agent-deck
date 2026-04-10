package ui

import (
	"strings"
	"testing"
)

func TestHelpOverlayHidesNotesShortcutWhenDisabled(t *testing.T) {
	disabled := false
	setPreviewShowNotesConfigForTest(t, &disabled)

	overlay := NewHelpOverlay()
	overlay.SetSize(100, 40)
	overlay.Show()

	view := overlay.View()
	if strings.Contains(view, "Edit notes") {
		t.Fatalf("help overlay should hide notes shortcut when show_notes=false, got %q", view)
	}
}

func TestHelpOverlayHidesNotesShortcutByDefault(t *testing.T) {
	// When no config is set (default), notes should be hidden.
	setPreviewShowNotesConfigForTest(t, nil)

	overlay := NewHelpOverlay()
	overlay.SetSize(100, 40)
	overlay.Show()

	view := overlay.View()
	if strings.Contains(view, "Edit notes") {
		t.Fatalf("help overlay should hide notes shortcut by default (not configured), got %q", view)
	}
}

func TestHelpOverlayShowsNotesShortcutWhenEnabled(t *testing.T) {
	enabled := true
	setPreviewShowNotesConfigForTest(t, &enabled)

	overlay := NewHelpOverlay()
	overlay.SetSize(100, 80)
	overlay.Show()

	view := overlay.View()
	if !strings.Contains(view, "Edit notes") {
		t.Fatalf("help overlay should show notes shortcut when show_notes=true, got %q", view)
	}
}
