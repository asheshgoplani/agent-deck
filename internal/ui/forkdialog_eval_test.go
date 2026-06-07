//go:build eval_smoke

package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestEval_ForkDialog_WithStateVisibleInteraction drives the real ForkDialog
// keystroke-by-keystroke (w -> y -> i) and asserts both that the with-state
// checkboxes render with the locked labels + hint and that the getters report
// the toggled values. This is the behavioral tier for PR-B gap 10 (TUI):
// it exercises the B2 focus model, the B3 rendering/key handlers, and the B1
// toggle invariants end-to-end through Update/View, not the individual units.
func TestEval_ForkDialog_WithStateVisibleInteraction(t *testing.T) {
	d := NewForkDialog()
	d.SetSize(90, 40)
	d.Show("Eval Parent", t.TempDir(), "", nil, "")
	// The eval drives dialog interaction only (no real worktree creation), so
	// mark the project worktree-capable regardless of the temp dir's VCS state.
	d.worktreeCapable = true

	press := func(k tea.KeyMsg) { d, _ = d.Update(k) }
	rune_ := func(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

	// name -> group
	press(tea.KeyMsg{Type: tea.KeyTab})
	if d.currentFocusName() != "group" {
		t.Fatalf("after one Tab, focus = %q, want group", d.currentFocusName())
	}

	// w: enable worktree (focus advances to the branch field)
	press(rune_('w'))
	if !d.IsWorktreeEnabled() {
		t.Fatal("pressing w on the group row should enable worktree mode")
	}

	// back to the group row so the y/i shortcuts are intercepted
	press(tea.KeyMsg{Type: tea.KeyShiftTab})
	if d.currentFocusName() != "group" {
		t.Fatalf("after Shift+Tab from branch, focus = %q, want group", d.currentFocusName())
	}

	// y: carry parent state on; i: include gitignored on
	press(rune_('y'))
	press(rune_('i'))

	if !d.IsWithStateEnabled() {
		t.Error("y should enable carry-parent-state")
	}
	if !d.IsWithStateAndGitignoredEnabled() {
		t.Error("i should enable include-gitignored")
	}

	view := d.View()
	for _, want := range []string{
		"Carry parent state",
		"creates a NEW branch at parent HEAD",
		"Include gitignored files",
		"[x] Carry parent state",
		"[x] Include gitignored files",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("rendered dialog missing %q after w->y->i; view:\n%s", want, view)
		}
	}
}
