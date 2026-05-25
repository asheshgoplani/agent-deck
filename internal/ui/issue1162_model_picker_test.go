package ui

// Regression tests for #1162 — TUI new-session model picker.
//
// Bug 1: typing a custom model name into the model picker showed no echo —
//        the suggestions dropdown overlay was painted directly on top of the
//        model input line, hiding whatever the user typed.
//
// Bug 2: pressing Esc while in the model picker closed the ENTIRE new-session
//        flow instead of dismissing only the picker. The Esc handler in
//        home.go's handleNewDialogKey called newDialog.Hide() before the
//        dialog could intercept Esc as a "close-self" for the picker.
//
// Reported via Feedback Hub by @wbonnefond on v1.9.30 (darwin/arm64, 2/5).

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// focusModelForCodex returns a visible new-session dialog with the codex tool
// selected and focus parked on the model field (the model picker is showing).
func focusModelForCodex(t *testing.T) *NewDialog {
	t.Helper()
	d := NewNewDialog()
	d.SetDefaultTool("codex")
	d.SetSize(100, 50)
	d.Show()
	d.focusIndex = d.indexOf(focusModel)
	if d.focusIndex < 0 {
		t.Fatal("codex should expose a focusable model field")
	}
	d.updateFocus()
	return d
}

// typeRunes feeds each rune of s to the dialog as individual key messages,
// mirroring real keystrokes through the Bubble Tea Update loop.
func typeRunes(d *NewDialog, s string) *NewDialog {
	for _, r := range s {
		d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return d
}

// --- Bug 1: typed custom model name must be visible ------------------------

// Happy path: a custom model ID that matches no known suggestion must still be
// echoed back in the rendered view (it can only show if the input line is not
// covered by the dropdown overlay).
func TestIssue1162_CustomModelNameVisibleWhileTyping(t *testing.T) {
	d := focusModelForCodex(t)
	const custom = "qwen3-custom-zzz"
	d = typeRunes(d, custom)

	if got := d.GetLaunchModelID(); got != custom {
		t.Fatalf("GetLaunchModelID() = %q, want %q", got, custom)
	}
	if view := d.View(); !strings.Contains(view, custom) {
		t.Fatalf("typed custom model %q not visible in rendered view (overlay hides input?):\n%s", custom, view)
	}
}

// Boundary: unicode custom model name (no suggestion match) must also echo.
func TestIssue1162_UnicodeModelNameVisibleWhileTyping(t *testing.T) {
	d := focusModelForCodex(t)
	const custom = "модель-абв"
	d = typeRunes(d, custom)

	if view := d.View(); !strings.Contains(view, custom) {
		t.Fatalf("unicode custom model %q not visible in rendered view:\n%s", custom, view)
	}
}

// --- Bug 2: Esc inside the picker dismisses only the picker ----------------

// IsModelPickerOpen reflects whether the picker is showing for the model field.
func TestIssue1162_IsModelPickerOpen(t *testing.T) {
	d := focusModelForCodex(t)
	if !d.IsModelPickerOpen() {
		t.Fatal("model picker should report open when focused on model field")
	}
	// A tool without model support (shell) must never report the picker open.
	shell := NewNewDialog()
	shell.SetDefaultTool("")
	shell.SetSize(100, 50)
	shell.Show()
	if shell.IsModelPickerOpen() {
		t.Fatal("shell tool has no model field; picker must not report open")
	}
}

// Dialog level: Esc while the picker is open dismisses the picker but keeps the
// dialog visible with focus still on the model field.
func TestIssue1162_EscDismissesPickerKeepsDialog(t *testing.T) {
	d := focusModelForCodex(t)
	d = typeRunes(d, "gpt-5.5")

	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if !d.IsVisible() {
		t.Fatal("Esc inside the model picker must NOT hide the new-session dialog")
	}
	if d.currentTarget() != focusModel {
		t.Fatalf("focus after Esc inside picker = %v, want focusModel", d.currentTarget())
	}
	if d.IsModelPickerOpen() {
		t.Fatal("Esc inside the picker should dismiss the picker dropdown")
	}
	// Typed value is preserved — only the picker closed.
	if got := d.GetLaunchModelID(); got != "gpt-5.5" {
		t.Fatalf("GetLaunchModelID() after dismiss = %q, want gpt-5.5", got)
	}
}

// Home level (the real reproduction): the parent intercepts Esc. When the
// picker is open, Esc must close only the picker; the form stays alive.
func TestIssue1162_HomeEscInPickerKeepsFlowAlive(t *testing.T) {
	h := NewHome()
	h.width, h.height = 100, 30
	h.newDialog.SetDefaultTool("codex")
	h.newDialog.SetSize(100, 50)
	h.newDialog.Show()
	h.newDialog.focusIndex = h.newDialog.indexOf(focusModel)
	h.newDialog.updateFocus()

	if !h.newDialog.IsModelPickerOpen() {
		t.Fatal("precondition: model picker should be open")
	}

	// First Esc: dismiss only the picker — flow must stay alive.
	h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyEsc})
	if !h.newDialog.IsVisible() {
		t.Fatal("Esc in picker closed the entire new-session flow (bug #1162)")
	}
	if h.newDialog.currentTarget() != focusModel {
		t.Fatalf("focus after Esc = %v, want focusModel", h.newDialog.currentTarget())
	}

	// Second Esc: picker already dismissed, so this cancels the flow.
	h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyEsc})
	if h.newDialog.IsVisible() {
		t.Fatal("second Esc (picker dismissed) should cancel the new-session flow")
	}
}

// Regression: Esc on the form itself (not in the picker) still cancels the flow.
func TestIssue1162_HomeEscOnFormCancelsFlow(t *testing.T) {
	h := NewHome()
	h.width, h.height = 100, 30
	h.newDialog.SetDefaultTool("codex")
	h.newDialog.SetSize(100, 50)
	h.newDialog.Show() // focus defaults to the name field, not the picker.

	if h.newDialog.IsModelPickerOpen() {
		t.Fatal("precondition: picker must be closed on the name field")
	}

	h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyEsc})
	if h.newDialog.IsVisible() {
		t.Fatal("Esc on the form (not picker) must cancel the whole flow")
	}
}
