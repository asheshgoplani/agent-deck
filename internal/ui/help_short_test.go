package ui

// ASK-211 onboarding (Sharjeel's prototype): '?' opens on "5 keys to start";
// Tab switches to all keys (scrollable with j/k) and back.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestHelpOverlayOpensOnFiveKeys(t *testing.T) {
	o := NewHelpOverlay()
	o.SetSize(80, 24)
	o.Show()
	view := stripAnsi(o.View())
	for _, want := range []string{
		"5 KEYS TO START",
		"Enter", "Open the selected session",
		"Ctrl+Q", "Come back to this list",
		"New session",
		"↑ ↓", "Move between sessions",
		"This help",
		"That is all you need today.",
		"Tab all keys", "Esc close",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("short help lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "QUICK START") {
		t.Fatalf("short help shows the full list:\n%s", view)
	}
	if lines := strings.Split(strings.TrimRight(view, "\n"), "\n"); len(lines) > 24 {
		t.Fatalf("short help is %d rows at 80x24", len(lines))
	}
}

func TestHelpOverlayTabSwitchesToAllKeysAndBack(t *testing.T) {
	o := NewHelpOverlay()
	o.SetSize(100, 200)
	o.Show()
	o.Update(tea.KeyMsg{Type: tea.KeyTab})
	if !o.IsVisible() || !o.ShowingAllKeys() {
		t.Fatal("Tab did not switch to all keys")
	}
	view := stripAnsi(o.View())
	for _, want := range []string{"KEYBOARD SHORTCUTS", "QUICK START", "GROUP NAVIGATION", "Tab 5 keys"} {
		if !strings.Contains(view, want) {
			t.Fatalf("all keys view lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "(v1.7.60)") {
		t.Fatal("the GROUP NAVIGATION heading still carries (v1.7.60)")
	}
	// Quick start gains n.
	quick := view[strings.Index(view, "QUICK START"):strings.Index(view, "NAVIGATION")]
	if !strings.Contains(quick, "New session") {
		t.Fatalf("Quick start lacks n / New session:\n%s", quick)
	}
	// j/k scroll in the full list (a short terminal, so it scrolls).
	o.SetSize(100, 30)
	if !strings.Contains(stripAnsi(o.View()), "j/k scroll") {
		t.Fatal("the scrolling full list does not say j/k scroll")
	}
	o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if o.scrollOffset != 1 || !o.IsVisible() {
		t.Fatalf("j did not scroll the full list (offset %d)", o.scrollOffset)
	}
	o.Update(tea.KeyMsg{Type: tea.KeyTab})
	if o.ShowingAllKeys() || !strings.Contains(stripAnsi(o.View()), "5 KEYS TO START") {
		t.Fatal("Tab did not switch back to 5 keys")
	}
}

func TestHelpOverlayShortViewClosesOnOtherKeys(t *testing.T) {
	for _, k := range []tea.KeyMsg{{Type: tea.KeyEsc}, {Type: tea.KeyRunes, Runes: []rune{'j'}}, {Type: tea.KeyRunes, Runes: []rune{'?'}}} {
		o := NewHelpOverlay()
		o.Show()
		o.Update(k)
		if o.IsVisible() {
			t.Fatalf("%q did not close the short help", k.String())
		}
	}
}

func TestHomeQuestionMarkOpensShortHelp(t *testing.T) {
	h := NewHome()
	h.width, h.height = 120, 40
	_, _ = h.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if !h.helpOverlay.IsVisible() || h.helpOverlay.ShowingAllKeys() {
		t.Fatal("? must open the help on 5 keys to start")
	}
}
