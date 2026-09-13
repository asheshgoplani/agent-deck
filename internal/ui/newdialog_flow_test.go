package ui

// New Session dialog flow: Enter walks the form, the Create button creates.
//
// Owner report (2026-09-12): "When I make a new session and go next, I get
// stuck on the model choosing step; and it prints automatically." Reproduced
// on v1.16.5 by driving the TUI: Enter on the Model row toggled its dropdown
// open/closed forever (never advanced), and once a model was picked the next
// Enter — on Reasoning effort, a checkbox, or any Claude Options row — created
// and launched the session on the spot. Tab also skipped every Claude Options
// row after the first.
//
// These tests pin the new contract: in Enter-advances mode (the default) Enter
// means "next" on every row, ↓/Space open the model list, Tab/Enter/↓ walk the
// tool options rows one at a time, and only the trailing "[ Create session ]"
// button (or Ctrl+S anywhere) submits.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func flowDialog(t *testing.T, tool string) *NewDialog {
	t.Helper()
	d := NewNewDialog()
	d.SetDefaultTool(tool)
	d.SetSize(120, 60)
	d.Show()
	d.nameInput.SetValue("demo")
	d.pathInput.SetValue(t.TempDir())
	return d
}

func flowFocus(t *testing.T, d *NewDialog, target focusTarget) {
	t.Helper()
	idx := d.indexOf(target)
	if idx < 0 {
		t.Fatalf("focus target %v is not in the form", target)
	}
	d.focusIndex = idx
	d.updateFocus()
}

func pressKey(d *NewDialog, key tea.KeyType) *NewDialog {
	d, _ = d.Update(tea.KeyMsg{Type: key})
	return d
}

// The reported loop: Enter on the Model row used to open the list, and Enter
// on its default "Type custom" entry closed it again, so Enter never left the
// row. Enter now accepts the field (empty = tool default) and advances.
func TestNewSessionFlow_EnterOnModelRowAdvances(t *testing.T) {
	d := flowDialog(t, "claude")
	flowFocus(t, d, focusModel)

	d = pressKey(d, tea.KeyEnter)
	if d.IsModelSuggestionsActive() {
		t.Fatal("Enter on the model row opened the dropdown instead of advancing (the reported loop)")
	}
	if d.currentTarget() != focusReasoningEffort {
		t.Fatalf("focus after Enter on Model = %v, want focusReasoningEffort", d.currentTarget())
	}
	if got := d.GetLaunchModelID(); got != "" {
		t.Fatalf("GetLaunchModelID() = %q, want empty (tool default)", got)
	}
}

// The other half of the loop: choosing "Type custom model ID…" keeps focus on
// the field (#1190) — the NEXT Enter must move on, not reopen the list.
func TestNewSessionFlow_EnterAfterTypeCustomAdvances(t *testing.T) {
	h := NewHome()
	h.width, h.height = 120, 40
	h.newDialog.SetDefaultTool("claude")
	h.newDialog.SetSize(120, 60)
	h.newDialog.Show()
	h.newDialog.nameInput.SetValue("demo")
	h.newDialog.pathInput.SetValue(t.TempDir())
	h.newDialog.focusIndex = h.newDialog.indexOf(focusModel)
	h.newDialog.updateFocus()

	space(h) // open the list
	if !h.newDialog.IsModelSuggestionsActive() || h.newDialog.modelSuggestionCursor != 0 {
		t.Fatal("Space should open the model list on the Type custom entry")
	}
	enter(h) // Type custom → back to the input
	if h.newDialog.currentTarget() != focusModel || h.newDialog.IsModelSuggestionsActive() {
		t.Fatal("Type custom should return to the model input (#1190)")
	}
	enter(h) // must advance now
	if h.newDialog.IsModelSuggestionsActive() {
		t.Fatal("second Enter reopened the model list (the reported loop)")
	}
	if !h.newDialog.IsVisible() {
		t.Fatal("second Enter submitted the form instead of advancing")
	}
	if h.newDialog.currentTarget() == focusModel {
		t.Fatal("second Enter did not leave the model row")
	}
}

// ↓ and Space are the ways into the model list; a picked entry still applies
// and advances (#1190 contract unchanged).
func TestNewSessionFlow_SpaceOpensModelListAndEnterPicks(t *testing.T) {
	d := flowDialog(t, "claude")
	flowFocus(t, d, focusModel)

	d = pressKey(d, tea.KeySpace)
	if !d.IsModelSuggestionsActive() {
		t.Fatal("Space on the model row should open the model list")
	}
	d = pressKey(d, tea.KeyDown)
	d = pressKey(d, tea.KeyEnter)
	if got := d.GetLaunchModelID(); got != "claude-opus-5" {
		t.Fatalf("GetLaunchModelID() = %q, want claude-opus-5", got)
	}
	if d.currentTarget() != focusReasoningEffort {
		t.Fatalf("focus after picking a model = %v, want focusReasoningEffort", d.currentTarget())
	}
}

// "it prints automatically": with a model picked, Enter on Reasoning effort
// (and every other non-text row) created the session immediately. Now every
// row hands Enter to the dialog, which advances.
func TestNewSessionFlow_EnterOnNonTextRowsAdvancesInsteadOfCreating(t *testing.T) {
	for _, target := range []focusTarget{focusCommand, focusReasoningEffort, focusWorktree, focusSandbox, focusMultiRepo, focusOptions} {
		d := flowDialog(t, "claude")
		flowFocus(t, d, target)
		if !d.shouldHandleEnterLocally() {
			t.Fatalf("%v: shouldHandleEnterLocally = false, want true (Enter must advance, not create)", target)
		}
		d = pressKey(d, tea.KeyEnter)
		if !d.IsVisible() {
			t.Fatalf("%v: Enter closed the dialog", target)
		}
		if target != focusOptions && d.currentTarget() == target {
			t.Fatalf("%v: Enter did not advance focus", target)
		}
	}
}

// Home-level reproduction of the auto-create: Enter on Reasoning effort must
// keep the form open with focus on the next row.
func TestNewSessionFlow_HomeEnterOnEffortDoesNotCreate(t *testing.T) {
	h := NewHome()
	h.width, h.height = 120, 40
	h.newDialog.SetDefaultTool("claude")
	h.newDialog.SetSize(120, 60)
	h.newDialog.Show()
	h.newDialog.nameInput.SetValue("demo")
	h.newDialog.pathInput.SetValue(t.TempDir())
	h.newDialog.focusIndex = h.newDialog.indexOf(focusReasoningEffort)
	h.newDialog.updateFocus()

	enter(h)
	if !h.newDialog.IsVisible() {
		t.Fatal("Enter on Reasoning effort created the session (dialog closed)")
	}
	if h.newDialog.currentTarget() != focusPath {
		t.Fatalf("focus after Enter on Reasoning effort = %v, want focusPath", h.newDialog.currentTarget())
	}
}

// The soft-selected path pre-fill is already usable: Enter advances (browse is
// Space/→), so a plain Enter walk does not stall on the Path row either.
func TestNewSessionFlow_EnterOnPrefilledPathAdvances(t *testing.T) {
	d := flowDialog(t, "claude")
	flowFocus(t, d, focusPath)
	if !d.pathSoftSelected {
		t.Fatal("precondition: a pre-filled path lands in soft-select")
	}
	d = pressKey(d, tea.KeyEnter)
	if d.IsSuggestionsActive() {
		t.Fatal("Enter on the pre-filled path opened browse instead of advancing")
	}
	if d.currentTarget() != focusWorktree {
		t.Fatalf("focus after Enter on Path = %v, want focusWorktree", d.currentTarget())
	}
}

// The Create button is the last focus target, the one row where Enter reaches
// home.go's submit path, and it is visible with its own footer.
func TestNewSessionFlow_CreateButtonIsLastAndSubmits(t *testing.T) {
	d := flowDialog(t, "claude")
	if last := d.focusTargets[len(d.focusTargets)-1]; last != focusCreate {
		t.Fatalf("last focus target = %v, want focusCreate", last)
	}
	view := stripAnsi(d.View())
	if !strings.Contains(view, "[ Create session ]") {
		t.Fatalf("Create button not rendered:\n%s", view)
	}

	flowFocus(t, d, focusCreate)
	if d.shouldHandleEnterLocally() {
		t.Fatal("shouldHandleEnterLocally on the Create button = true, want false (Enter must submit)")
	}
	view = stripAnsi(d.View())
	if !strings.Contains(view, "▶ [ Create session ]") || !strings.Contains(view, "Enter create") {
		t.Fatalf("focused Create button not highlighted with an Enter create hint:\n%s", view)
	}
}

// Tab used to jump from the first Claude Options row straight back to Name;
// now it walks every option row, leaves for the Create button, and Shift+Tab
// re-enters the panel at its last row.
func TestNewSessionFlow_TabWalksClaudeOptionsRows(t *testing.T) {
	d := flowDialog(t, "claude")
	flowFocus(t, d, focusOptions)
	panel := d.claudeOptions
	if !panel.IsFocused() || panel.focusIndex != 0 {
		t.Fatal("precondition: options panel focused on its first row")
	}

	rows := panel.getFocusCount()
	for i := 1; i < rows; i++ {
		d = pressKey(d, tea.KeyTab)
		if d.currentTarget() != focusOptions || panel.focusIndex != i {
			t.Fatalf("after %d Tabs: target=%v panel row=%d, want focusOptions row %d", i, d.currentTarget(), panel.focusIndex, i)
		}
	}
	d = pressKey(d, tea.KeyTab)
	if d.currentTarget() != focusCreate {
		t.Fatalf("Tab past the last option row landed on %v, want focusCreate", d.currentTarget())
	}
	d = pressKey(d, tea.KeyShiftTab)
	if d.currentTarget() != focusOptions || panel.focusIndex != rows-1 {
		t.Fatalf("Shift+Tab from Create: target=%v panel row=%d, want focusOptions row %d", d.currentTarget(), panel.focusIndex, rows-1)
	}
}

// Enter and ↓ inside the options panel step one row at a time; Enter past the
// last row lands on the Create button.
func TestNewSessionFlow_EnterAndDownWalkClaudeOptionsRows(t *testing.T) {
	d := flowDialog(t, "claude")
	flowFocus(t, d, focusOptions)
	panel := d.claudeOptions

	d = pressKey(d, tea.KeyDown)
	if d.currentTarget() != focusOptions || panel.focusIndex != 1 {
		t.Fatalf("↓ on the first option row: target=%v row=%d, want focusOptions row 1", d.currentTarget(), panel.focusIndex)
	}
	d = pressKey(d, tea.KeyEnter)
	if d.currentTarget() != focusOptions || panel.focusIndex != 2 {
		t.Fatalf("Enter on an option row: target=%v row=%d, want focusOptions row 2", d.currentTarget(), panel.focusIndex)
	}
	panel.FocusLast()
	d = pressKey(d, tea.KeyEnter)
	if d.currentTarget() != focusCreate {
		t.Fatalf("Enter on the last option row landed on %v, want focusCreate", d.currentTarget())
	}
}

// Explicit opt-out ([ui] new_session_enter_advances = false) keeps the legacy
// contract: Enter creates from any row, including the Model row.
func TestNewSessionFlow_LegacyEnterCreatesFromModelRow(t *testing.T) {
	d := flowDialog(t, "claude")
	d.enterAdvances = false
	for _, target := range []focusTarget{focusModel, focusReasoningEffort, focusOptions, focusCreate} {
		flowFocus(t, d, target)
		if d.shouldHandleEnterLocally() {
			t.Fatalf("legacy mode: %v handles Enter locally, want submit", target)
		}
	}
	// An open model list still owns Enter in legacy mode.
	flowFocus(t, d, focusModel)
	d = pressKey(d, tea.KeySpace)
	if !d.shouldHandleEnterLocally() {
		t.Fatal("legacy mode: an open model list must still own Enter")
	}
}

// Every row's footer says what Enter does there; the model field no longer
// shows an example ID that reads as a preselected model.
func TestNewSessionFlow_FooterHintsAndNeutralPlaceholder(t *testing.T) {
	d := flowDialog(t, "claude")
	cases := []struct {
		target focusTarget
		want   []string
	}{
		{focusName, []string{"Enter next", "^S create"}},
		{focusCommand, []string{"Enter next", "^S create"}},
		{focusModel, []string{"↓/Space browse IDs", "Enter next", "tool default"}},
		{focusReasoningEffort, []string{"Enter next", "^S create"}},
		{focusWorktree, []string{"Enter next", "^S create"}},
		{focusOptions, []string{"Enter next", "^S create"}},
		{focusCreate, []string{"Enter create"}},
	}
	for _, tc := range cases {
		flowFocus(t, d, tc.target)
		view := stripAnsi(d.View())
		for _, want := range tc.want {
			if !strings.Contains(view, want) {
				t.Fatalf("%v: view lacks %q:\n%s", tc.target, want, view)
			}
		}
		if strings.Contains(view, "> claude-sonnet-4-6") {
			t.Fatalf("%v: model field shows an example ID as its value:\n%s", tc.target, view)
		}
	}
	if strings.Contains(stripAnsi(d.claudeOptions.View()), "--model opus") {
		t.Fatal("Extra args placeholder still suggests --model opus (the Model row owns the model)")
	}
}
