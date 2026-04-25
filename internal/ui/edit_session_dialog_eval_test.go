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
		Command:   "claude --verbose",
		Color:     "#aabbcc",
		Notes:     "eval notes line",
		Channels:  []string{"plugin:telegram@eval/repo"},
		ExtraArgs: []string{"--model", "haiku"},
	}
	d.Show(inst)

	// Exercise at least one Update cycle so focus/blur paths are covered —
	// this is the shape the runtime passes through after Show.
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyTab})
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyShiftTab})

	view := d.View()
	want := []string{
		"Edit Session Settings", // dialog title
		"my-eval-session",       // Title field prepopulated
		"claude --verbose",      // Command field prepopulated
		"#aabbcc",               // Color field prepopulated
		"eval notes line",       // Notes field prepopulated
		"plugin:telegram@eval/repo",
		"--model haiku",
		"tool: claude",
	}
	for _, tok := range want {
		if !strings.Contains(view, tok) {
			t.Errorf("View() missing expected token %q.\nFull view:\n%s", tok, view)
		}
	}
	// The claude-specific hint must be visible for a claude session so the
	// user understands which fields require --channels/--extra-args wiring.
	if !strings.Contains(view, "claude") {
		t.Error("View() must mention claude somewhere for a claude-tool session")
	}
}

func TestEval_EditSessionDialog_ShellToolHidesClaudeOnlyFields(t *testing.T) {
	d := NewEditSessionDialog()
	d.SetSize(100, 40)
	d.Show(&session.Instance{
		ID:    "sess-eval-shell",
		Title: "shell-session",
		Tool:  "shell",
	})

	view := d.View()
	for _, forbidden := range []string{"Channels", "Extra args"} {
		if strings.Contains(view, forbidden) {
			t.Errorf("shell-tool session should not render %q field; rendered view:\n%s", forbidden, view)
		}
	}
	// The restart-required Command/Wrapper/Tool fields must still appear —
	// they're tool-agnostic.
	for _, required := range []string{"Command", "Wrapper", "Tool"} {
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
func TestEval_EditSessionDialog_RestartHintSurfacesForRestartFields(t *testing.T) {
	d := NewEditSessionDialog()
	d.SetSize(100, 40)
	inst := &session.Instance{
		ID:      "sess-eval-restart",
		Title:   "restart-test",
		Tool:    "claude",
		Command: "claude",
	}
	d.Show(inst)

	// Locate Command by key — index-based lookup would silently reroute
	// typing if Show() order ever changed.
	commandIdx := -1
	for i, f := range d.fields {
		if f.key == session.FieldCommand {
			commandIdx = i
			break
		}
	}
	if commandIdx < 0 {
		t.Fatal("Command field not found in dialog")
	}
	d.focusIndex = commandIdx
	d.updateFocus()

	// Clear Command and type a new one.
	for range "claude" {
		d, _ = d.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	for _, r := range "claude --new-flag" {
		d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	if !d.HasRestartRequiredChanges(inst) {
		t.Error("editing Command must flag HasRestartRequiredChanges=true; home.go relies on this to prompt the user")
	}
	changes := d.GetChanges(inst)
	var commandChange *Change
	for i := range changes {
		if changes[i].Field == session.FieldCommand {
			commandChange = &changes[i]
			break
		}
	}
	if commandChange == nil {
		t.Fatalf("GetChanges did not include Command edit; got %+v", changes)
	}
	if commandChange.IsLive {
		t.Error("Command change must carry IsLive=false so home.go labels it restart-required")
	}
	if commandChange.Value != "claude --new-flag" {
		t.Errorf("Command change value = %q, want %q", commandChange.Value, "claude --new-flag")
	}
}
