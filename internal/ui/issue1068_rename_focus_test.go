package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Issue #1068: rename-group dialog's text input is unresponsive when opened
// after the Create-group dialog has previously tabbed focus to the path input.
//
// Root cause: ShowRename() (and ShowRenameSession()) call nameInput.Focus()
// but leave focusIndex at whatever value the last Create dialog set it to
// (focusIndex=1 after Tab). Update() then routes keystrokes to the invisible
// pathInput. Restart wipes the focusIndex and "fixes" the bug.
//
// The fix must guarantee that opening Rename / RenameSession always lands
// focus on nameInput regardless of leftover dialog state.

// TestIssue1068_RenameAfterCreateTab_AcceptsKeystrokes reproduces the bug
// at the GroupDialog level using only the public API.
func TestIssue1068_RenameAfterCreateTab_AcceptsKeystrokes(t *testing.T) {
	g := NewGroupDialog()
	g.SetSize(100, 30)

	// Step 1: User opens Create-Subgroup dialog (CanToggle is false here, so
	// Tab moves focus to the path input rather than toggling).
	g.ShowCreateSubgroup("parent", "Parent")

	// Step 2: User Tabs to the optional "Default Path" field.
	g, _ = g.Update(tea.KeyMsg{Type: tea.KeyTab})

	// Step 3: User cancels the Create dialog (mimics pressing Esc → Hide).
	g.Hide()

	// Step 4: User presses 'r' on a group → ShowRename is called.
	g.ShowRename("some/group", "old-name")
	if !g.IsVisible() {
		t.Fatal("Rename dialog should be visible after ShowRename")
	}
	if g.Mode() != GroupDialogRename {
		t.Fatalf("Mode = %v, want GroupDialogRename", g.Mode())
	}

	// Step 5: User types 'a'. With the bug, the key is routed to the
	// invisible pathInput and the name value never changes. With the fix,
	// the key lands on the visible nameInput.
	g, _ = g.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})

	got := g.GetValue()
	want := "old-namea"
	if got != want {
		t.Errorf("GetValue() after typing 'a' = %q, want %q (key was routed to pathInput instead of nameInput)", got, want)
	}
}

// TestIssue1068_RenameSessionAfterCreateTab_AcceptsKeystrokes covers the
// same stale-focus regression for the rename-session path, since
// ShowRenameSession has the identical defect as ShowRename.
func TestIssue1068_RenameSessionAfterCreateTab_AcceptsKeystrokes(t *testing.T) {
	g := NewGroupDialog()
	g.SetSize(100, 30)

	g.ShowCreateSubgroup("parent", "Parent")
	g, _ = g.Update(tea.KeyMsg{Type: tea.KeyTab})
	g.Hide()

	g.ShowRenameSession("sess-1", "old-title")
	if g.Mode() != GroupDialogRenameSession {
		t.Fatalf("Mode = %v, want GroupDialogRenameSession", g.Mode())
	}

	g, _ = g.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})

	got := g.GetValue()
	want := "old-titleX"
	if got != want {
		t.Errorf("GetValue() after typing 'X' = %q, want %q", got, want)
	}
}
