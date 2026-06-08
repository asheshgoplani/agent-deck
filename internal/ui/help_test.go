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

func TestWrapWithHangingIndent_ShortText_NoWrap(t *testing.T) {
	got := wrapWithHangingIndent("Short text", 40, "    ")
	want := "Short text"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWrapWithHangingIndent_LongText_HangingIndent(t *testing.T) {
	indent := strings.Repeat(" ", 16)
	got := wrapWithHangingIndent("Filter search scoped to current group", 20, indent)
	lines := strings.Split(got, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected wrapped output, got single line: %q", got)
	}
	for i, l := range lines[1:] {
		if !strings.HasPrefix(l, indent) {
			t.Errorf("continuation line %d missing hanging indent: %q", i+1, l)
		}
	}
	for i, l := range lines {
		visible := l
		if i > 0 {
			visible = strings.TrimPrefix(l, indent)
		}
		if len(visible) > 20 {
			t.Errorf("line %d exceeds width 20: %q (visible=%d)", i, l, len(visible))
		}
	}
}

func TestWrapWithHangingIndent_EmptyString(t *testing.T) {
	got := wrapWithHangingIndent("", 40, "  ")
	if got != "" {
		t.Fatalf("got %q, want empty string", got)
	}
}

func TestWrapWithHangingIndent_SingleLongWord_NoInfiniteLoop(t *testing.T) {
	got := wrapWithHangingIndent("Supercalifragilisticexpialidocious", 10, "  ")
	if got == "" {
		t.Fatal("expected output, got empty string")
	}
}

func TestWrapWithHangingIndent_ZeroOrNegativeWidth_ReturnsInput(t *testing.T) {
	for _, w := range []int{0, -1, -10} {
		got := wrapWithHangingIndent("anything goes here", w, "  ")
		if got != "anything goes here" {
			t.Errorf("width=%d: got %q, want input verbatim", w, got)
		}
	}
}
