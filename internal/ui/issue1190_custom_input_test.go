package ui

// Regression tests for #1190 — TUI new-session dialog: the free-text "custom
// path" and "custom model ID" inputs did not accept typed input.
//
// Root cause (NOT #1162): #1023 (per-session model selection, extending #772's
// path dropdown) made Enter on the synthetic "✎ Type custom …" entry call
// moveFocus(1). Selecting the custom entry therefore JUMPED focus to the next
// form field instead of landing the cursor in the now-focused text input, so
// the field the user expected to type into was already blurred. Direct typing
// worked, but the discoverable UI path (open dropdown → pick "Custom" → type)
// was dead. #1162 fixed echo-visibility + Esc-scoping but never this focus jump.
//
// These tests drive through home.handleNewDialogKey (the real key-routing entry
// point) — the #1162 tests only called NewDialog.Update directly and so never
// caught the parent-routed dropdown→custom→type flow.
//
// Reported by @marekaf on v1.9.32 (macOS, iTerm2 + tmux 3.6a).

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func enter(h *Home)  { h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyEnter}) }
func down(h *Home)   { h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyDown}) }
func escKey(h *Home) { h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyEsc}) }

func typeHome(h *Home, s string) {
	for _, r := range s {
		h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

// --- Custom path: select "Type custom path…" then type -----------------------

// Happy path: opening the path dropdown, selecting the synthetic "Type custom
// path…" entry with Enter, must keep focus on the path field and accept typed
// input (value updates + visible in the rendered view).
func TestIssue1190_CustomPathSelectThenType(t *testing.T) {
	h := NewHome()
	h.width, h.height = 120, 40
	h.newDialog.SetDefaultTool("")
	h.newDialog.SetSize(120, 50)
	h.newDialog.Show()
	h.newDialog.focusIndex = h.newDialog.indexOf(focusPath)
	h.newDialog.pathInput.SetValue("") // emulate an empty custom-path scenario
	h.newDialog.updateFocus()

	enter(h) // open the suggestions dropdown
	if !h.newDialog.IsSuggestionsActive() {
		t.Fatal("Enter on the path field should open the path suggestions dropdown")
	}
	if h.newDialog.pathSuggestionCursor != 0 {
		t.Fatalf("dropdown should start on the synthetic custom entry (cursor 0), got %d", h.newDialog.pathSuggestionCursor)
	}

	enter(h) // select "✎ Type custom path…"
	if got := h.newDialog.currentTarget(); got != focusPath {
		t.Fatalf("after selecting Type custom path, focus = %v, want focusPath (focus must NOT jump away)", got)
	}

	const custom = "~/my/custom-path-zzz"
	typeHome(h, custom)
	if got := h.newDialog.pathInput.Value(); got != custom {
		t.Fatalf("custom path value = %q, want %q", got, custom)
	}
	if view := h.newDialog.View(); !strings.Contains(view, custom) {
		t.Fatalf("typed custom path %q not visible in rendered view:\n%s", custom, view)
	}
}

// --- Custom model: select "Type custom model ID…" then type ------------------

// Happy path: selecting the synthetic custom-model entry with Enter keeps focus
// on the model field and accepts typed input (don't regress #1162 echo).
func TestIssue1190_CustomModelSelectThenType(t *testing.T) {
	h := NewHome()
	h.width, h.height = 120, 40
	h.newDialog.SetDefaultTool("codex")
	h.newDialog.SetSize(120, 50)
	h.newDialog.Show()
	h.newDialog.focusIndex = h.newDialog.indexOf(focusModel)
	if h.newDialog.focusIndex < 0 {
		t.Fatal("codex should expose a focusable model field")
	}
	h.newDialog.modelInput.SetValue("")
	h.newDialog.updateFocus()

	enter(h) // open the model dropdown
	if !h.newDialog.IsModelSuggestionsActive() {
		t.Fatal("Enter on the model field should open the model suggestions dropdown")
	}
	if h.newDialog.modelSuggestionCursor != 0 {
		t.Fatalf("model dropdown should start on the synthetic custom entry (cursor 0), got %d", h.newDialog.modelSuggestionCursor)
	}

	enter(h) // select "✎ Type custom model ID…"
	if got := h.newDialog.currentTarget(); got != focusModel {
		t.Fatalf("after selecting Type custom model ID, focus = %v, want focusModel (focus must NOT jump away)", got)
	}

	const custom = "qwen3-custom-zzz"
	typeHome(h, custom)
	if got := h.newDialog.GetLaunchModelID(); got != custom {
		t.Fatalf("custom model value = %q, want %q", got, custom)
	}
	if view := h.newDialog.View(); !strings.Contains(view, custom) {
		t.Fatalf("typed custom model %q not visible in rendered view:\n%s", custom, view)
	}
}

// --- Regression guards: real-suggestion Enter STILL advances focus -----------

// Selecting a real path suggestion (cursor > 0) with Enter must still apply it
// and advance focus — the #1190 fix is scoped to the synthetic custom entry.
func TestIssue1190_RealPathSuggestionEnterStillAdvances(t *testing.T) {
	h := NewHome()
	h.width, h.height = 120, 40
	h.newDialog.SetDefaultTool("")
	h.newDialog.SetSize(120, 50)
	h.newDialog.Show()
	h.newDialog.SetPathSuggestions([]string{"/tmp/project-a", "/tmp/project-b"})
	h.newDialog.focusIndex = h.newDialog.indexOf(focusPath)
	h.newDialog.pathInput.SetValue("")
	h.newDialog.updateFocus()

	enter(h) // open dropdown
	down(h)  // move to first real suggestion (cursor 1)
	if h.newDialog.pathSuggestionCursor != 1 {
		t.Fatalf("down should move to first real suggestion (cursor 1), got %d", h.newDialog.pathSuggestionCursor)
	}
	enter(h) // accept real suggestion
	if got := h.newDialog.pathInput.Value(); got != "/tmp/project-a" {
		t.Fatalf("selected path = %q, want /tmp/project-a", got)
	}
	if got := h.newDialog.currentTarget(); got == focusPath {
		t.Fatal("Enter on a REAL path suggestion should advance focus off the path field")
	}
}

// Selecting a real model suggestion (cursor > 0) with Enter must still apply it
// and advance focus (mirrors existing #1023 behavior; guards the #1190 fix).
func TestIssue1190_RealModelSuggestionEnterStillAdvances(t *testing.T) {
	h := NewHome()
	h.width, h.height = 120, 40
	h.newDialog.SetDefaultTool("codex")
	h.newDialog.SetSize(120, 50)
	h.newDialog.Show()
	h.newDialog.focusIndex = h.newDialog.indexOf(focusModel)
	h.newDialog.modelInput.SetValue("")
	h.newDialog.updateFocus()

	enter(h) // open dropdown
	down(h)  // move to first real suggestion (cursor 1)
	if h.newDialog.modelSuggestionCursor != 1 {
		t.Fatalf("down should move to first real model suggestion (cursor 1), got %d", h.newDialog.modelSuggestionCursor)
	}
	enter(h) // accept real suggestion
	if got := h.newDialog.GetLaunchModelID(); got == "" {
		t.Fatal("Enter on a real model suggestion should apply it")
	}
	if got := h.newDialog.currentTarget(); got == focusModel {
		t.Fatal("Enter on a REAL model suggestion should advance focus off the model field")
	}
}

// --- #1162 regression guards (must keep passing) -----------------------------

// Esc inside the model picker dismisses only the picker, keeping the flow alive.
func TestIssue1190_EscInModelPickerDismissesOnlyPicker(t *testing.T) {
	h := NewHome()
	h.width, h.height = 120, 40
	h.newDialog.SetDefaultTool("codex")
	h.newDialog.SetSize(120, 50)
	h.newDialog.Show()
	h.newDialog.focusIndex = h.newDialog.indexOf(focusModel)
	h.newDialog.updateFocus()

	if !h.newDialog.IsModelPickerOpen() {
		t.Fatal("precondition: model picker should be open on the model field")
	}
	escKey(h)
	if !h.newDialog.IsVisible() {
		t.Fatal("Esc in the picker must NOT cancel the new-session flow (#1162)")
	}
	if h.newDialog.currentTarget() != focusModel {
		t.Fatalf("focus after Esc in picker = %v, want focusModel (#1162)", h.newDialog.currentTarget())
	}
	escKey(h)
	if h.newDialog.IsVisible() {
		t.Fatal("second Esc (picker dismissed) should cancel the flow (#1162)")
	}
}

// Esc on the form (name field, no picker) still cancels the whole flow.
func TestIssue1190_EscOnFormCancels(t *testing.T) {
	h := NewHome()
	h.width, h.height = 120, 40
	h.newDialog.SetDefaultTool("codex")
	h.newDialog.SetSize(120, 50)
	h.newDialog.Show() // focus defaults to the name field

	if h.newDialog.IsModelPickerOpen() {
		t.Fatal("precondition: picker must be closed on the name field")
	}
	escKey(h)
	if h.newDialog.IsVisible() {
		t.Fatal("Esc on the form (not picker) must cancel the whole flow (#1162)")
	}
}
