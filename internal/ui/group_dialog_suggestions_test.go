package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func keyRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestGroupDialogSuggestions_ProviderLoadedOnShow(t *testing.T) {
	g := NewGroupDialog()
	corpus := []string{"/home/me/agent-deck", "/home/me/other-project"}
	g.SetSuggestProvider(func() []string { return corpus })

	g.Show()
	if len(g.allPathSuggestions) != 2 {
		t.Fatalf("expected provider corpus loaded on Show, got %v", g.allPathSuggestions)
	}
	// Path field is not focused yet (name has focus): no dropdown.
	if g.IsPathDropdownVisible() {
		t.Fatal("dropdown must not be visible while the name field is focused")
	}

	g.focusPath()
	if !g.IsPathDropdownVisible() {
		t.Fatal("dropdown should be visible when the path field is focused with candidates")
	}
	if g.pathSuggestionCursor != 0 {
		t.Fatalf("first candidate should be highlighted, got cursor %d", g.pathSuggestionCursor)
	}
}

func TestGroupDialogSuggestions_LiveFuzzyFiltering(t *testing.T) {
	g := NewGroupDialog()
	g.Show()
	g.SetPathSuggestions([]string{
		"/home/me/projects/agent-deck",
		"/home/me/documents",
	})
	g.focusPath()

	g.Update(keyRunes("a"))
	g.Update(keyRunes("d"))
	g.Update(keyRunes("k"))

	if len(g.pathSuggestions) != 1 || g.pathSuggestions[0] != "/home/me/projects/agent-deck" {
		t.Fatalf("expected fuzzy hit for 'adk', got %v", g.pathSuggestions)
	}
	if !g.IsPathDropdownVisible() || g.pathSuggestionCursor != 0 {
		t.Fatalf("dropdown should stay visible with first hit highlighted, cursor=%d", g.pathSuggestionCursor)
	}
}

func TestGroupDialogSuggestions_NavigateAndAcceptWithTab(t *testing.T) {
	g := NewGroupDialog()
	g.Show()
	g.SetPathSuggestions([]string{"/alpha", "/beta"})
	g.focusPath()

	// Down moves highlight to the second entry.
	g.Update(tea.KeyMsg{Type: tea.KeyDown})
	if g.pathSuggestionCursor != 1 {
		t.Fatalf("cursor = %d, want 1 after Down", g.pathSuggestionCursor)
	}

	g.Update(tea.KeyMsg{Type: tea.KeyTab})
	if got := g.GetDefaultPath(); got != "/beta" {
		t.Fatalf("Tab should accept the highlighted suggestion, got %q", got)
	}
	if g.IsPathDropdownVisible() {
		t.Fatal("dropdown should hide after acceptance")
	}
}

func TestGroupDialogSuggestions_EnterConsumedByParentGuard(t *testing.T) {
	g := NewGroupDialog()
	g.Show()
	g.SetPathSuggestions([]string{"/alpha"})
	g.focusPath()

	if !g.ApplyHighlightedPathSuggestion() {
		t.Fatal("Enter guard should consume when a suggestion is highlighted")
	}
	if got := g.GetDefaultPath(); got != "/alpha" {
		t.Fatalf("suggestion value not applied, got %q", got)
	}
	// After acceptance the next Enter must reach normal submit handling.
	if g.ApplyHighlightedPathSuggestion() {
		t.Fatal("guard must not consume twice; dropdown is dismissed")
	}
}

func TestGroupDialogSuggestions_EscDismissesThenTypingReopens(t *testing.T) {
	g := NewGroupDialog()
	g.Show()
	g.SetPathSuggestions([]string{"/alpha", "/beta"})
	g.focusPath()

	if !g.DismissPathSuggestions() {
		t.Fatal("Esc should dismiss a visible dropdown")
	}
	if g.DismissPathSuggestions() {
		t.Fatal("second Esc should fall through to dialog close")
	}
	if g.IsPathDropdownVisible() {
		t.Fatal("dropdown should be hidden after Esc")
	}

	// Typing re-opens live filtering.
	g.Update(keyRunes("a"))
	if !g.IsPathDropdownVisible() {
		t.Fatal("typing should reopen the dropdown after dismissal")
	}
}

func TestGroupDialogSuggestions_UpDownFallThroughToToggleWithoutMatches(t *testing.T) {
	g := NewGroupDialog()
	g.ShowCreateWithContext("/work/team", "team") // Tab toggle available
	g.SetPathSuggestions([]string{"/alpha"})
	g.focusPath()

	// Filter down to nothing so the dropdown cannot navigate.
	g.Update(keyRunes("z"))
	g.Update(keyRunes("z"))
	g.Update(keyRunes("z"))
	if len(g.pathSuggestions) != 0 {
		t.Fatalf("expected zero matches, got %v", g.pathSuggestions)
	}

	before := g.groupPath
	g.Update(tea.KeyMsg{Type: tea.KeyUp})
	if g.groupPath == before {
		t.Fatal("Up should toggle Root/Subgroup when there is nothing to navigate")
	}
}

func TestGroupDialogSuggestions_ViewRendersDropdownOverlay(t *testing.T) {
	g := NewGroupDialog()
	g.SetSize(100, 40)
	g.Show()
	g.SetPathSuggestions([]string{"/home/me/agent-deck"})
	g.focusPath()

	view := g.View()
	if !strings.Contains(view, "/home/me/agent-deck") {
		t.Fatal("rendered view should contain the path suggestion overlay")
	}
	if !strings.Contains(view, "Tab/Enter accept") {
		t.Fatal("rendered dropdown footer hints missing")
	}
}

func TestGroupDialogSuggestions_ResetOnRenameMode(t *testing.T) {
	g := NewGroupDialog()
	g.Show()
	g.SetPathSuggestions([]string{"/alpha"})
	g.focusPath()

	g.ShowRename("/old", "old-name")
	if len(g.pathSuggestions) != 0 {
		t.Fatalf("rename mode should clear suggestions, got %v", g.pathSuggestions)
	}
	if g.IsPathDropdownVisible() {
		t.Fatal("no dropdown in rename mode")
	}
}
