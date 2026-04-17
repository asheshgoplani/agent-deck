package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestGroupDialog_NameInput_AcceptsUnderscore verifies that typing '_' into the
// group name input reaches the textinput buffer (regression test for BUG-02).
func TestGroupDialog_NameInput_AcceptsUnderscore(t *testing.T) {
	g := NewGroupDialog()
	g.Show()

	underscoreKey := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'_'}}
	updated, _ := g.Update(underscoreKey)

	if updated.nameInput.Value() != "_" {
		t.Errorf("nameInput.Value() = %q after typing '_', want %q", updated.nameInput.Value(), "_")
	}
}

// TestGroupDialog_ShowRenameSession_CursorAtEnd_Issue604 verifies that after a
// prior rename with a shorter name, opening ShowRenameSession with a longer
// name places the cursor at the end of the new name, not at the stale cursor
// position clamped to the new length. Regression test for issue #604.
func TestGroupDialog_ShowRenameSession_CursorAtEnd_Issue604(t *testing.T) {
	g := NewGroupDialog()

	// First rename: short name; user types a bit, then "saves" (we simulate
	// only the cursor state leak — no actual save needed).
	g.ShowRenameSession("sess-1", "alpha") // 5 chars
	// Cursor should be at end (5) initially.
	if pos := g.nameInput.Position(); pos != len("alpha") {
		t.Fatalf("first ShowRenameSession: initial cursor = %d, want %d", pos, len("alpha"))
	}
	// Simulate user editing: move cursor to position 2 (e.g. by pressing
	// left arrow a few times or clicking).
	g.nameInput.SetCursor(2)
	g.Hide()

	// Second rename: a longer name. Cursor should go to end of new name.
	longName := "delta-epsilon-zeta-eta" // 22 chars
	g.ShowRenameSession("sess-2", longName)

	if pos := g.nameInput.Position(); pos != len(longName) {
		t.Errorf("second ShowRenameSession: cursor = %d, want %d (end of %q)",
			pos, len(longName), longName)
	}
}

// TestGroupDialog_ShowRename_CursorAtEnd_Issue604 is the same regression as
// above but for the group-rename entry point.
func TestGroupDialog_ShowRename_CursorAtEnd_Issue604(t *testing.T) {
	g := NewGroupDialog()

	g.ShowRename("/a", "alpha")
	g.nameInput.SetCursor(2)
	g.Hide()

	longName := "some-much-longer-group-name"
	g.ShowRename("/b", longName)

	if pos := g.nameInput.Position(); pos != len(longName) {
		t.Errorf("second ShowRename: cursor = %d, want %d (end of %q)",
			pos, len(longName), longName)
	}
}

// TestGroupDialog_ShowRenameSession_FreshCursor_Issue604 verifies that after a
// Create-then-type cycle (which leaves the cursor advanced), opening a rename
// resets the cursor to the end of the pre-filled name — not to wherever the
// cursor was left from the Create dialog.
func TestGroupDialog_ShowRenameSession_FreshCursor_Issue604(t *testing.T) {
	g := NewGroupDialog()

	g.Show() // create mode, empty name
	// Simulate typing 3 characters: cursor advances to 3.
	for _, r := range "abc" {
		key := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		updated, _ := g.Update(key)
		g = updated
	}
	if pos := g.nameInput.Position(); pos != 3 {
		t.Fatalf("sanity: after typing 'abc' cursor = %d, want 3", pos)
	}
	g.Hide()

	name := "my-session"
	g.ShowRenameSession("sess-X", name)

	if pos := g.nameInput.Position(); pos != len(name) {
		t.Errorf("ShowRenameSession after Create-type cycle: cursor = %d, want %d",
			pos, len(name))
	}
}
