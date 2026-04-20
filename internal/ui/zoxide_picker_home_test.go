package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestDeriveSessionNameFromPath_UsesBase(t *testing.T) {

	got := deriveSessionNameFromPath("/Users/me/projects/goat")

	if got != "goat" {
		t.Fatalf("deriveSessionNameFromPath = %q, want goat", got)
	}
}

func TestDeriveSessionNameFromPath_TrailingSlashStripped(t *testing.T) {

	got := deriveSessionNameFromPath("/Users/me/projects/goat/")

	if got != "goat" {
		t.Fatalf("deriveSessionNameFromPath = %q, want goat", got)
	}
}

func TestDeriveSessionNameFromPath_EmptyFallsBackToGenerated(t *testing.T) {

	got := deriveSessionNameFromPath("")

	if got == "" || strings.ContainsAny(got, "/.") {
		t.Fatalf("expected a generated fallback name, got %q", got)
	}
}

func TestEnsureUniqueSessionTitle_NoCollision(t *testing.T) {
	instances := []*session.Instance{{Title: "existing"}}

	got := ensureUniqueSessionTitle("goat", instances)

	if got != "goat" {
		t.Fatalf("ensureUniqueSessionTitle = %q, want goat", got)
	}
}

func TestEnsureUniqueSessionTitle_SuffixesOnCollision(t *testing.T) {
	instances := []*session.Instance{
		{Title: "goat"},
		{Title: "goat-2"},
	}

	got := ensureUniqueSessionTitle("goat", instances)

	if got != "goat-3" {
		t.Fatalf("ensureUniqueSessionTitle = %q, want goat-3", got)
	}
}

func TestHome_ZoxidePickerInitialized(t *testing.T) {
	home := NewHome()

	if home.zoxidePicker == nil {
		t.Fatal("zoxidePicker should be initialized")
	}
	if home.zoxidePicker.IsVisible() {
		t.Fatal("zoxidePicker should start hidden")
	}
}

func TestHome_ZPressOpensPicker(t *testing.T) {
	home := NewHome()

	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})

	h, ok := model.(*Home)
	if !ok {
		t.Fatalf("expected *Home, got %T", model)
	}
	if !h.zoxidePicker.IsVisible() {
		t.Fatal("expected picker to be visible after pressing z")
	}
}

func TestHome_ZoxidePickerEscClosesWithoutCreating(t *testing.T) {
	home := NewHome()
	home.zoxidePicker = newZoxidePickerWithQueryFn(stubQueryFn([]string{"/some/path"}, nil))
	home.zoxidePicker.Show()

	model, _ := home.handleZoxidePickerKey(tea.KeyMsg{Type: tea.KeyEsc})

	h := model.(*Home)
	if h.zoxidePicker.IsVisible() {
		t.Fatal("expected picker hidden after esc")
	}
}

func TestHome_HasModalVisibleIncludesPicker(t *testing.T) {
	home := NewHome()
	home.initialLoading = false // clear the splash so the picker bit is what we observe

	if home.hasModalVisible() {
		t.Fatal("expected no modals visible before showing picker")
	}

	home.zoxidePicker.Show()

	if !home.hasModalVisible() {
		t.Fatal("hasModalVisible should be true while picker is open")
	}
}
