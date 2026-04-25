package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

func sampleInstance() *session.Instance {
	return &session.Instance{
		ID:                 "sess-1",
		Title:              "my-session",
		Tool:               "claude",
		Command:            "claude",
		Wrapper:            "",
		Color:              "#ff00aa",
		Notes:              "initial notes",
		Channels:           []string{"plugin:telegram@owner/repo"},
		ExtraArgs:          []string{"--model", "opus"},
		TitleLocked:        false,
		NoTransitionNotify: false,
	}
}

func TestEditSessionDialog_InitiallyHidden(t *testing.T) {
	d := NewEditSessionDialog()
	if d == nil {
		t.Fatal("NewEditSessionDialog returned nil")
	}
	if d.IsVisible() {
		t.Error("dialog should be hidden after construction")
	}
	if got := d.View(); got != "" {
		t.Errorf("View() should return empty string when hidden, got %q", got)
	}
}

// Without this, blank fields would wipe the session to zero on save.
func TestEditSessionDialog_ShowPopulatesFromInstance(t *testing.T) {
	d := NewEditSessionDialog()
	inst := sampleInstance()
	d.Show(inst)

	if !d.IsVisible() {
		t.Fatal("dialog should be visible after Show()")
	}
	if d.SessionID() != inst.ID {
		t.Errorf("SessionID() = %q, want %q", d.SessionID(), inst.ID)
	}

	view := d.View()
	for _, want := range []string{"my-session", "#ff00aa", "initial notes", "plugin:telegram@owner/repo", "--model opus"} {
		if !strings.Contains(view, want) {
			t.Errorf("View() should contain %q; got:\n%s", want, view)
		}
	}
}

func TestEditSessionDialog_Hide(t *testing.T) {
	d := NewEditSessionDialog()
	d.Show(sampleInstance())
	d.Hide()
	if d.IsVisible() {
		t.Error("dialog should be hidden after Hide()")
	}
}

// Tab/Shift+Tab must wrap, not clamp — a clamp would strand the user
// on the first or last field.
func TestEditSessionDialog_TabCyclesFocus(t *testing.T) {
	d := NewEditSessionDialog()
	d.Show(sampleInstance())
	total := len(d.fields)
	if total == 0 {
		t.Fatal("expected at least one field after Show()")
	}

	// Tab forward: cycles through all fields and wraps back to 0.
	for i := 1; i <= total; i++ {
		d, _ = d.Update(tea.KeyMsg{Type: tea.KeyTab})
		want := i % total
		if d.focusIndex != want {
			t.Fatalf("after %d Tab(s), focusIndex=%d, want %d", i, d.focusIndex, want)
		}
	}

	// Shift+Tab wraps backward from 0.
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if d.focusIndex != total-1 {
		t.Fatalf("Shift+Tab should wrap to last (%d), got %d", total-1, d.focusIndex)
	}
}

func TestEditSessionDialog_SpaceTogglesCheckbox(t *testing.T) {
	d := NewEditSessionDialog()
	d.Show(sampleInstance())

	// Find the TitleLocked checkbox and focus it.
	found := -1
	for i, f := range d.fields {
		if f.key == session.FieldTitleLocked {
			found = i
			break
		}
	}
	if found < 0 {
		t.Fatal("expected TitleLocked field present")
	}
	d.focusIndex = found
	d.updateFocus()

	if d.fields[found].checked {
		t.Fatal("TitleLocked should start false (sampleInstance sets it false)")
	}
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	if !d.fields[found].checked {
		t.Error("space should toggle checkbox from false to true")
	}
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	if d.fields[found].checked {
		t.Error("space should toggle checkbox back to false")
	}
}

func TestEditSessionDialog_TypingUpdatesFocusedInput(t *testing.T) {
	d := NewEditSessionDialog()
	d.Show(sampleInstance())

	d.focusIndex = 0 // Title by construction
	d.updateFocus()

	// Clear the field (simulate backspace N times) then type.
	title := d.fields[0].input.Value()
	for range title {
		d, _ = d.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	for _, r := range "renamed" {
		d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	if got := d.fields[0].input.Value(); got != "renamed" {
		t.Errorf("Title input value = %q, want %q", got, "renamed")
	}
}

func TestEditSessionDialog_GetChanges_EmptyWhenUnchanged(t *testing.T) {
	d := NewEditSessionDialog()
	inst := sampleInstance()
	d.Show(inst)
	changes := d.GetChanges(inst)
	if len(changes) != 0 {
		t.Errorf("GetChanges on untouched dialog = %d changes, want 0: %v", len(changes), changes)
	}
}

func TestEditSessionDialog_GetChanges_DetectsTextEdit(t *testing.T) {
	d := NewEditSessionDialog()
	inst := sampleInstance()
	d.Show(inst)

	// Edit title.
	for i := range d.fields {
		if d.fields[i].key == session.FieldTitle {
			d.fields[i].input.SetValue("new-title")
			break
		}
	}

	changes := d.GetChanges(inst)
	if len(changes) != 1 {
		t.Fatalf("got %d changes, want 1: %v", len(changes), changes)
	}
	c := changes[0]
	if c.Field != session.FieldTitle || c.Value != "new-title" || !c.IsLive {
		t.Errorf("got %+v, want Field=title Value=new-title IsLive=true", c)
	}
}

func TestEditSessionDialog_GetChanges_DetectsCheckboxToggle(t *testing.T) {
	d := NewEditSessionDialog()
	inst := sampleInstance()
	d.Show(inst)

	for i := range d.fields {
		if d.fields[i].key == session.FieldTitleLocked {
			d.fields[i].checked = true
			break
		}
	}

	changes := d.GetChanges(inst)
	if len(changes) != 1 {
		t.Fatalf("got %d changes, want 1: %v", len(changes), changes)
	}
	c := changes[0]
	if c.Field != session.FieldTitleLocked || c.Value != "true" || !c.IsLive || !c.Checkbox {
		t.Errorf("got %+v, want Field=title-locked Value=true IsLive=true Checkbox=true", c)
	}
}

func TestEditSessionDialog_HasRestartRequiredChanges(t *testing.T) {
	d := NewEditSessionDialog()
	inst := sampleInstance()
	d.Show(inst)

	// Touch only live Title.
	for i := range d.fields {
		if d.fields[i].key == session.FieldTitle {
			d.fields[i].input.SetValue("renamed")
			break
		}
	}
	if d.HasRestartRequiredChanges(inst) {
		t.Error("Title-only edit must not flag restart-required")
	}

	// Touch restart-required Command.
	for i := range d.fields {
		if d.fields[i].key == session.FieldCommand {
			d.fields[i].input.SetValue("claude --new-flag")
			break
		}
	}
	if !d.HasRestartRequiredChanges(inst) {
		t.Error("Command edit must flag restart-required")
	}
}

func TestEditSessionDialog_Validate_Title(t *testing.T) {
	d := NewEditSessionDialog()
	d.Show(sampleInstance())
	for i := range d.fields {
		if d.fields[i].key == session.FieldTitle {
			d.fields[i].input.SetValue("   ")
			break
		}
	}
	if msg := d.Validate(); !strings.Contains(strings.ToLower(msg), "title") {
		t.Errorf("Validate() = %q, should mention empty title", msg)
	}
}

func TestEditSessionDialog_Validate_InvalidColor(t *testing.T) {
	d := NewEditSessionDialog()
	d.Show(sampleInstance())
	for i := range d.fields {
		if d.fields[i].key == session.FieldColor {
			d.fields[i].input.SetValue("red")
			break
		}
	}
	if msg := d.Validate(); !strings.Contains(strings.ToLower(msg), "color") {
		t.Errorf("Validate() = %q, should mention invalid color", msg)
	}
}

// Hiding claude-only fields for shell/gemini sessions is friendlier
// UX than letting the user submit and watch SetField reject them.
func TestEditSessionDialog_ChannelsHiddenForNonClaudeTool(t *testing.T) {
	inst := sampleInstance()
	inst.Tool = "shell"
	inst.Channels = nil
	inst.ExtraArgs = nil

	d := NewEditSessionDialog()
	d.Show(inst)

	for _, f := range d.fields {
		if f.key == session.FieldChannels || f.key == session.FieldExtraArgs {
			t.Errorf("field %q should be hidden for non-claude tool %q", f.key, inst.Tool)
		}
	}
}

// Esc/Enter must reach the outer router as commit/cancel intent — the
// dialog must not absorb them.
func TestEditSessionDialog_EscReturnsWithoutSwallow(t *testing.T) {
	d := NewEditSessionDialog()
	inst := sampleInstance()
	d.Show(inst)

	d, cmd := d.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Errorf("Esc should not emit a tea.Cmd; got %v", cmd)
	}
	if len(d.GetChanges(inst)) != 0 {
		t.Error("Esc should not mutate dialog state")
	}
}

func TestEditSessionDialog_EnterReturnsWithoutSwallow(t *testing.T) {
	d := NewEditSessionDialog()
	inst := sampleInstance()
	d.Show(inst)

	d, cmd := d.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Errorf("Enter should not emit a tea.Cmd; got %v", cmd)
	}
	if len(d.GetChanges(inst)) != 0 {
		t.Error("Enter alone (no edits) should leave changes empty")
	}
}

func TestEditSessionDialog_SetErrorRendersInline(t *testing.T) {
	d := NewEditSessionDialog()
	d.Show(sampleInstance())
	d.SetError("channels only supported for claude sessions")

	if !strings.Contains(d.View(), "channels only supported") {
		t.Error("View() should display the error message set via SetError")
	}

	d.ClearError()
	if strings.Contains(d.View(), "channels only supported") {
		t.Error("ClearError should remove the inline error")
	}
}

// Space toggles a focused checkbox but must reach a focused text input
// as a literal — titles with spaces have to be typable.
func TestEditSessionDialog_SpaceInsideTextInput(t *testing.T) {
	d := NewEditSessionDialog()
	d.Show(sampleInstance())

	// Find title (text) and set focus on it.
	for i := range d.fields {
		if d.fields[i].key == session.FieldTitle {
			d.focusIndex = i
			break
		}
	}
	d.updateFocus()

	// Clear, then type "a b".
	title := d.fields[d.focusIndex].input.Value()
	for range title {
		d, _ = d.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	for _, r := range "a b" {
		d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	if got := d.fields[d.focusIndex].input.Value(); got != "a b" {
		t.Errorf("text input should accept literal space; got %q, want %q", got, "a b")
	}
}

// A stale error from a prior Show() must not bleed into a new session.
func TestEditSessionDialog_ReShowResetsError(t *testing.T) {
	d := NewEditSessionDialog()
	d.Show(sampleInstance())
	d.SetError("previous boom")

	other := sampleInstance()
	other.ID = "sess-2"
	other.Title = "other"
	d.Show(other)

	if strings.Contains(d.View(), "previous boom") {
		t.Error("Show() should clear any inline error from a prior Show()")
	}
}
