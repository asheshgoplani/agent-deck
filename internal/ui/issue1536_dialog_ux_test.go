package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Issue #1536 — TUI dialog UX pass.
//
// Case 1 (group create): with a group/session context open the Root/Subgroup
// toggle was bound to Tab on the name field, so forward-Tab kept toggling and
// never reached the Default Path field — trapping the user. Fix: Tab always
// advances focus; the toggle is rebound to Up/Down.
//
// Case 2 (new-session path): after typing a custom directory, Enter on the Path
// field re-opened the browse dropdown (only Ctrl+S proceeded), so it felt like a
// loop. Fix: Enter on an actively-typed path that already resolves to an existing
// directory advances focus; Enter still browses for the soft-selected pre-fill
// and for empty/non-existent paths.

// Case 1a: Tab from the name field reaches the Default Path field (does NOT
// toggle Root/Subgroup) when a group context makes the toggle available.
func TestIssue1536_GroupCreate_TabReachesDefaultPath(t *testing.T) {
	g := NewGroupDialog()
	// Cursor on a session inside a group → defaults to root, toggle available.
	g.ShowCreateWithContextDefaultRoot("work", "work")

	if !g.CanToggle() {
		t.Fatal("precondition: CanToggle() should be true with a group context")
	}
	if g.focusIndex != 0 {
		t.Fatalf("precondition: initial focus = %d, want 0 (name)", g.focusIndex)
	}
	rootBefore := g.GetGroupPath()

	// Type the group name.
	for _, r := range "projects" {
		updated, _ := g.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		g = updated
	}

	// Forward Tab must move focus to the Default Path field, NOT toggle the mode.
	updated, _ := g.Update(tea.KeyMsg{Type: tea.KeyTab})
	g = updated

	if g.focusIndex != 1 {
		t.Fatalf("after Tab, focusIndex = %d, want 1 (Default Path) — Tab is trapped on the toggle", g.focusIndex)
	}
	if g.GetGroupPath() != rootBefore {
		t.Fatalf("Tab toggled Root/Subgroup (groupPath %q → %q); Tab must only move focus", rootBefore, g.GetGroupPath())
	}

	// The path field must now accept input.
	tmpRepo := t.TempDir()
	for _, r := range tmpRepo {
		updated, _ := g.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		g = updated
	}
	if got := g.GetDefaultPath(); got != tmpRepo {
		t.Fatalf("GetDefaultPath() = %q, want %q — Default Path unreachable via Tab", got, tmpRepo)
	}
}

// Case 1b: Up/Down toggles Root/Subgroup (the rebound binding) without moving
// text-input focus.
func TestIssue1536_GroupCreate_UpDownToggles(t *testing.T) {
	g := NewGroupDialog()
	g.ShowCreateWithContextDefaultRoot("work", "work")

	if g.GetGroupPath() != "" {
		t.Fatalf("precondition: expected root mode (empty groupPath), got %q", g.GetGroupPath())
	}

	// Down switches root → subgroup.
	updated, _ := g.Update(tea.KeyMsg{Type: tea.KeyDown})
	g = updated
	if g.GetGroupPath() != "work" {
		t.Fatalf("after Down, groupPath = %q, want \"work\" (subgroup)", g.GetGroupPath())
	}
	if g.focusIndex != 0 {
		t.Fatalf("Down changed focus (focusIndex=%d); toggle must not move focus", g.focusIndex)
	}

	// Up switches subgroup → root.
	updated, _ = g.Update(tea.KeyMsg{Type: tea.KeyUp})
	g = updated
	if g.GetGroupPath() != "" {
		t.Fatalf("after Up, groupPath = %q, want \"\" (root)", g.GetGroupPath())
	}
}

// Case 2a: Enter on a typed, existing-directory path advances focus instead of
// re-opening the browse dropdown.
func TestIssue1536_NewDialog_EnterOnValidPathAdvances(t *testing.T) {
	tmpDir := t.TempDir()

	d := NewNewDialog()
	d.SetSize(100, 50)
	d.Show()
	d.focusIndex = d.indexOf(focusPath)
	d.updateFocus()

	// Simulate a user-typed custom path (not the soft-selected pre-fill).
	d.pathSoftSelected = false
	d.pathInput.SetValue(tmpDir)

	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if d.IsSuggestionsActive() {
		t.Fatal("Enter on a typed existing-directory path reopened the browse dropdown (the reported loop)")
	}
	if d.currentTarget() == focusPath {
		t.Fatal("Enter on a valid typed path did not advance focus off the Path field")
	}
}

// Case 2b: Enter still opens the browse dropdown for the soft-selected pre-fill
// and for empty/non-existent paths (browse remains useful there).
func TestIssue1536_NewDialog_EnterStillBrowsesWhenAppropriate(t *testing.T) {
	tmpDir := t.TempDir()

	// Soft-selected pre-fill → Enter browses.
	d := NewNewDialog()
	d.SetSize(100, 50)
	d.Show()
	d.focusIndex = d.indexOf(focusPath)
	d.updateFocus()
	d.pathSoftSelected = true
	d.pathInput.SetValue(tmpDir)
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !d.IsSuggestionsActive() {
		t.Fatal("Enter on the soft-selected pre-fill should still open the browse dropdown")
	}

	// Non-existent typed path → Enter browses (path not yet usable).
	d2 := NewNewDialog()
	d2.SetSize(100, 50)
	d2.Show()
	d2.focusIndex = d2.indexOf(focusPath)
	d2.updateFocus()
	d2.pathSoftSelected = false
	d2.pathInput.SetValue("/no/such/directory/issue1536")
	d2, _ = d2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !d2.IsSuggestionsActive() {
		t.Fatal("Enter on a non-existent typed path should still open the browse dropdown")
	}
}
