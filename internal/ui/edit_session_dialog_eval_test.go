//go:build eval_smoke

package ui

// Behavioral eval for the TUI EditSessionDialog (CLAUDE.md:82-108 mandate
// for interactive prompts). Lives in internal/ui/ because Go's internal-
// package rule blocks tests/eval/... from importing it; still runs under
// `-tags eval_smoke`. See tests/eval/README.md.

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

// Empty-form regression: a refactor that reset textinputs after Show
// would leave fields blank and the next save would zero the instance.
func TestEval_EditSessionDialog_ShowRendersCurrentInstanceValues(t *testing.T) {
	d := NewEditSessionDialog()
	d.SetSize(100, 40)
	inst := &session.Instance{
		ID:        "sess-eval-1",
		Title:     "my-eval-session",
		Tool:      "claude",
		ExtraArgs: []string{"--model", "haiku"},
		GroupPath: "personal/scratch",
	}
	d.Show(inst)

	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyTab})
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyShiftTab})

	view := d.View()
	want := []string{
		"Edit Session",    // dialog title (parity with "New Session")
		"in group:",       // group header parity with newdialog
		"scratch",         // last segment of GroupPath
		"my-eval-session", // Title prepopulated
		"--model haiku",
		"Skip permissions",
		"Auto mode",
	}
	for _, tok := range want {
		if !strings.Contains(view, tok) {
			t.Errorf("View() missing expected token %q.\nFull view:\n%s", tok, view)
		}
	}
}

// Shell sessions have no claude-specific extra args — surfacing them would
// only lead to a SetField "claude only" rejection on submit.
func TestEval_EditSessionDialog_ShellToolHidesExtraArgs(t *testing.T) {
	d := NewEditSessionDialog()
	d.SetSize(100, 40)
	d.Show(&session.Instance{
		ID:    "sess-eval-shell",
		Title: "shell-session",
		Tool:  "shell",
	})

	view := d.View()
	if strings.Contains(view, "Extra args") {
		t.Errorf("shell-tool session should not render Extra args; rendered view:\n%s", view)
	}
	for _, required := range []string{"Title", "Tool"} {
		if !strings.Contains(view, required) {
			t.Errorf("shell-tool session should still render %q; got:\n%s", required, view)
		}
	}
}

// "Why isn't my rename applying?" — a misplaced return in the navigation
// switch could swallow runes before they reach the textinput, leaving the
// dialog looking edited but GetChanges empty.
func TestEval_EditSessionDialog_TypingAndEnterProducesChange(t *testing.T) {
	d := NewEditSessionDialog()
	d.SetSize(100, 40)
	inst := &session.Instance{
		ID:    "sess-eval-type",
		Title: "original",
		Tool:  "claude",
	}
	d.Show(inst)

	// Title is the first field — focus is already there after Show.
	for range "original" {
		d, _ = d.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	for _, r := range "renamed" {
		d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyEnter})

	changes := d.GetChanges(inst)
	if len(changes) != 1 || changes[0].Field != session.FieldTitle || changes[0].Value != "renamed" {
		t.Fatalf("expected single title change to 'renamed'; got %+v", changes)
	}
	// The rendered frame must show the typed value — a broken View() would
	// still show "original" and a user screenshot would look broken to them.
	if !strings.Contains(d.View(), "renamed") {
		t.Errorf("View() after typing should contain 'renamed'; got:\n%s", d.View())
	}
}

// home.go reads HasRestartRequiredChanges to decide on the "press R to
// restart" hint. False negative → user wonders why edits don't apply.
// ExtraArgs is the canonical restart-required text field in the slim set.
func TestEval_EditSessionDialog_RestartHintSurfacesForRestartFields(t *testing.T) {
	d := NewEditSessionDialog()
	d.SetSize(100, 40)
	inst := &session.Instance{
		ID:    "sess-eval-restart",
		Title: "restart-test",
		Tool:  "claude",
	}
	d.Show(inst)

	idx := -1
	for i, f := range d.fields {
		if f.key == session.FieldExtraArgs {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("ExtraArgs field not found in dialog")
	}
	d.focusIndex = idx
	d.updateFocus()

	for _, r := range "--model haiku" {
		d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	if !d.HasRestartRequiredChanges(inst) {
		t.Error("editing ExtraArgs must flag HasRestartRequiredChanges=true; home.go relies on this to prompt the user")
	}
	changes := d.GetChanges(inst)
	var extraChange *Change
	for i := range changes {
		if changes[i].Field == session.FieldExtraArgs {
			extraChange = &changes[i]
			break
		}
	}
	if extraChange == nil {
		t.Fatalf("GetChanges did not include ExtraArgs edit; got %+v", changes)
	}
	if extraChange.IsLive {
		t.Error("ExtraArgs change must carry IsLive=false so home.go labels it restart-required")
	}
	if extraChange.Value != "--model haiku" {
		t.Errorf("ExtraArgs change value = %q, want %q", extraChange.Value, "--model haiku")
	}
}
