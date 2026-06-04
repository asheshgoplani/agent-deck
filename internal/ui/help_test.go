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

func TestHelpOverlayShowsCommitHash(t *testing.T) {
	prevV, prevC := Version, Commit
	t.Cleanup(func() { Version, Commit = prevV, prevC })

	Version = "1.9.45"
	Commit = "ab44d360"

	overlay := NewHelpOverlay()
	overlay.SetSize(120, 400) // tall enough that the version footer is not below the scroll fold
	overlay.Show()

	view := overlay.View()
	if !strings.Contains(view, "Agent Deck v1.9.45 (ab44d360)") {
		t.Fatalf("help overlay should show version with commit hash, got %q", view)
	}
}

func TestHelpOverlayOmitsCommitWhenVersionEmbedsIt(t *testing.T) {
	// Local `make build` bakes `git describe` into Version (e.g.
	// 1.9.45-49-gab44d360), which already ends in the short hash. The commit
	// must not be appended a second time.
	prevV, prevC := Version, Commit
	t.Cleanup(func() { Version, Commit = prevV, prevC })

	Version = "1.9.45-49-gab44d360"
	Commit = "ab44d360"

	overlay := NewHelpOverlay()
	overlay.SetSize(120, 400) // tall enough that the version footer renders
	overlay.Show()

	view := overlay.View()
	// The version footer must render (so the assertion is meaningful), and the
	// commit must appear exactly once — embedded in the version, not appended.
	if !strings.Contains(view, "Agent Deck v1.9.45-49-gab44d360") {
		t.Fatalf("version footer not rendered, got %q", view)
	}
	if strings.Contains(view, "(ab44d360)") {
		t.Fatalf("help overlay should not append commit when version already embeds it, got %q", view)
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
