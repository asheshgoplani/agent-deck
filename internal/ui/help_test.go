package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestHelpOverlayQuickStartUsesCanonicalRecoveryAndDetachNames(t *testing.T) {
	overlay := NewHelpOverlay()
	overlay.SetSize(100, 120)
	overlay.Show()

	view := overlay.View()
	for _, want := range []string{"QUICK START", "R", "Restart selected session", "Ctrl+Q", "Detach from session"} {
		if !strings.Contains(view, want) {
			t.Fatalf("help Quick Start missing %q, got %q", want, view)
		}
	}
	if strings.Contains(view, "$              Filter errors") {
		t.Fatalf("help must not advertise $ as the error filter, got %q", view)
	}
}

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

// TestHelpOverlayShowsArchiveKeys is the regression for the #1325 help gap: the
// archive family (A / Shift+U / ^) was reachable in the TUI and shown in the
// top filter bar but missing from the `?` help overlay.
func TestHelpOverlayShowsArchiveKeys(t *testing.T) {
	overlay := NewHelpOverlay()
	overlay.SetSize(100, 120) // tall enough to render the full SESSIONS section
	overlay.Show()

	view := overlay.View()
	for _, want := range []string{"Archive session", "Unarchive session", "Toggle archived view"} {
		if !strings.Contains(view, want) {
			t.Fatalf("help overlay should document archive key %q (#1325), got %q", want, view)
		}
	}
	// The archive default keybindings must surface too. Keys render exactly as
	// stored in the keymap (lowercase chord names, matching ctrl+z / ctrl+r).
	for _, key := range []string{"shift+u", "^"} {
		if !strings.Contains(view, key) {
			t.Fatalf("help overlay should show archive keybinding %q, got %q", key, view)
		}
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

// The copy family (#1595, #1412, #791) was previously scattered through the
// 30-row SESSIONS block, where nobody found it — the recurring user question
// was "why can't I select text?". The keys now live in their own section that
// also documents the terminal-level Shift+drag bypass of agent-deck's mouse
// capture (tea.WithMouseCellMotion), which is the actual answer to that
// question and is not a keybinding at all.
func TestHelpOverlayShowsCopySection(t *testing.T) {
	overlay := NewHelpOverlay()
	overlay.SetSize(100, 200) // tall enough to render every section
	overlay.Show()

	view := overlay.View()

	if !strings.Contains(view, "COPY & TEXT SELECTION") {
		t.Fatalf("help overlay should carry a dedicated copy section, got %q", view)
	}

	for _, want := range []string{
		"Copy last AI response",
		"Copy session info (repo / path / branch)",
		"Copy visible terminal text, including links",
		"Copy a fenced code block (picker if several)",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("copy section missing description %q, got %q", want, view)
		}
	}

	// The Shift+drag row is a terminal-level hint, not a binding: agent-deck
	// holds mouse mode 1002 so the terminal never sees a drag as selection.
	if !strings.Contains(view, "Shift+drag") {
		t.Fatalf("copy section must document the Shift+drag selection bypass, got %q", view)
	}
	if !strings.Contains(view, "Option+drag in iTerm2") {
		t.Fatalf("copy section must document the iTerm2 bypass, got %q", view)
	}
}

// y is bound in defaultHotkeyBindings but appeared in neither the help overlay
// nor any reference doc — the only fully undocumented shortcut in the keymap.
func TestHelpOverlayShowsYoloToggle(t *testing.T) {
	overlay := NewHelpOverlay()
	overlay.SetSize(100, 200)
	overlay.Show()

	if view := overlay.View(); !strings.Contains(view, "Toggle YOLO mode") {
		t.Fatalf("help overlay should document the yolo toggle, got %q", view)
	}
}

// TestHelpOverlayWrappedKeyColumnKeepsAlignmentAndSeparator: a
// three-alternative key label (e.g. "+ / K / Shift+↑") used to be hard-wrapped
// by the key column's fixed Width() into a misaligned block, and a key label
// that exactly filled the column (e.g. "--group <name>") glued onto its
// description with no separating space.
func TestHelpOverlayWrappedKeyColumnKeepsAlignmentAndSeparator(t *testing.T) {
	// Tall enough that no scrolling is needed, so a key/description pair
	// that wraps across rows is never split across a scroll-page boundary.
	overlay := NewHelpOverlay()
	overlay.SetSize(200, 300)
	overlay.Show()

	view := overlay.View()

	if strings.Contains(view, "<name>Launch") {
		t.Errorf("STARTUP FLAGS key/description glued with no space: %q", view)
	}

	found := false
	lines := strings.Split(view, "\n")
	for i, line := range lines {
		if !strings.Contains(line, "+ / K /") || !strings.Contains(line, "Reorder up") || i+1 >= len(lines) {
			continue
		}
		found = true
		// The continuation ("Shift+↑") must be the very next row, aligned
		// under the key column (not flush at the dialog's left margin, and
		// not carrying a second, duplicate description).
		cont := lines[i+1]
		if !strings.Contains(cont, "Shift+↑") {
			t.Fatalf("expected 'Shift+↑' continuation on the row after %q, got %q", line, cont)
		}
		if strings.Contains(cont, "Reorder") {
			t.Errorf("continuation row %q should not repeat the description", cont)
		}
		if keyIdx, contIdx := strings.Index(line, "+ / K /"), strings.Index(cont, "Shift+↑"); keyIdx != contIdx {
			t.Errorf("continuation 'Shift+↑' not aligned under the key column: key at %d, continuation at %d", keyIdx, contIdx)
		}
	}
	if !found {
		t.Fatalf("expected wrapped '+ / K / Shift+↑' key column in help view, got %q", view)
	}
}

// TestHelpOverlayMoreBelowClearsAtTrueEnd: the "▼ more below" indicator used
// to stay lit forever once scrolled, because the scroll-offset clamp assumed a
// bigger content budget than the render path had once indicator rows were
// reserved, so the last page could never be reached.
func TestHelpOverlayMoreBelowClearsAtTrueEnd(t *testing.T) {
	overlay := NewHelpOverlay()
	overlay.SetSize(200, 60)
	overlay.Show()

	// Scroll far past the end, as "j" held down would.
	for i := 0; i < 200; i++ {
		overlay, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	}

	view := overlay.View()
	if strings.Contains(view, "▼ more below") {
		t.Errorf("help overlay still shows '▼ more below' at the true end of content: %q", view)
	}
	if !strings.Contains(view, "STARTUP FLAGS") {
		t.Fatalf("expected to have scrolled to the final section, got %q", view)
	}

	// Further presses must be idempotent (no further movement possible).
	before := overlay.View()
	overlay, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	after := overlay.View()
	if before != after {
		t.Errorf("view should be stable once at the true end; got a change after another 'j'")
	}
}
