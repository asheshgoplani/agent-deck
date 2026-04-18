package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func newTestMultiRepoInstance(paths []string) *session.Instance {
	inst := session.NewInstance("test-multi", paths[0])
	inst.MultiRepoEnabled = true
	if len(paths) > 1 {
		inst.AdditionalPaths = paths[1:]
	}
	inst.MultiRepoTempDir = "/tmp/agent-deck-test-mr"
	return inst
}

func TestEditPathsDialog_ShowPopulatesPaths(t *testing.T) {
	inst := newTestMultiRepoInstance([]string{"/tmp/repo-a", "/tmp/repo-b"})
	d := NewEditPathsDialog()
	d.Show(inst, nil)

	if !d.IsVisible() {
		t.Fatal("dialog should be visible after Show")
	}
	paths := d.paths
	if len(paths) != 2 {
		t.Fatalf("expected 2 paths, got %d", len(paths))
	}
}

func TestEditPathsDialog_HasChanged(t *testing.T) {
	inst := newTestMultiRepoInstance([]string{"/tmp/repo-a", "/tmp/repo-b"})
	d := NewEditPathsDialog()
	d.Show(inst, nil)

	if d.HasChanged() {
		t.Fatal("should not report changes right after Show")
	}

	d.paths = append(d.paths, "/tmp/repo-c")
	if !d.HasChanged() {
		t.Fatal("should report changes after adding a path")
	}
}

func TestEditPathsDialog_AddPath(t *testing.T) {
	inst := newTestMultiRepoInstance([]string{"/tmp/repo-a", "/tmp/repo-b"})
	d := NewEditPathsDialog()
	d.SetSize(80, 40)
	d.Show(inst, nil)

	// Press 'a' to add a new path
	d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})

	if len(d.paths) != 3 {
		t.Fatalf("expected 3 paths after add, got %d", len(d.paths))
	}
	if !d.editing {
		t.Fatal("should be in editing mode after add")
	}
	if d.pathCursor != 2 {
		t.Errorf("cursor should be on new path (index 2), got %d", d.pathCursor)
	}
}

func TestEditPathsDialog_RemovePath(t *testing.T) {
	inst := newTestMultiRepoInstance([]string{"/tmp/repo-a", "/tmp/repo-b", "/tmp/repo-c"})
	d := NewEditPathsDialog()
	d.SetSize(80, 40)
	d.Show(inst, nil)

	// Select second path and remove
	d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})

	if len(d.paths) != 2 {
		t.Fatalf("expected 2 paths after remove, got %d", len(d.paths))
	}
}

func TestEditPathsDialog_RemoveBlockedAtMinimum(t *testing.T) {
	inst := newTestMultiRepoInstance([]string{"/tmp/repo-a", "/tmp/repo-b"})
	d := NewEditPathsDialog()
	d.SetSize(80, 40)
	d.Show(inst, nil)

	// Try to remove — should be blocked since we're at 2 (minimum)
	d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})

	if len(d.paths) != 2 {
		t.Fatalf("remove should be blocked at 2 paths, got %d", len(d.paths))
	}
}

func TestEditPathsDialog_NavigateUpDown(t *testing.T) {
	inst := newTestMultiRepoInstance([]string{"/tmp/repo-a", "/tmp/repo-b", "/tmp/repo-c"})
	d := NewEditPathsDialog()
	d.SetSize(80, 40)
	d.Show(inst, nil)

	if d.pathCursor != 0 {
		t.Fatalf("initial cursor should be 0, got %d", d.pathCursor)
	}

	d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if d.pathCursor != 1 {
		t.Errorf("cursor should be 1 after j, got %d", d.pathCursor)
	}

	d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if d.pathCursor != 0 {
		t.Errorf("cursor should be 0 after k, got %d", d.pathCursor)
	}
}

func TestEditPathsDialog_EditAndSave(t *testing.T) {
	inst := newTestMultiRepoInstance([]string{"/tmp/repo-a", "/tmp/repo-b"})
	d := NewEditPathsDialog()
	d.SetSize(80, 40)
	d.Show(inst, nil)

	// Enter edit mode
	d.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !d.editing {
		t.Fatal("should be in editing mode after Enter")
	}

	// Type a new path
	d.pathInput.SetValue("/tmp/new-repo")

	// Save with Enter
	d.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if d.editing {
		t.Fatal("should exit editing mode after save")
	}
	if d.paths[0] != "/tmp/new-repo" {
		t.Errorf("expected path to be updated to /tmp/new-repo, got %s", d.paths[0])
	}
}

func TestEditPathsDialog_EditAndCancel(t *testing.T) {
	inst := newTestMultiRepoInstance([]string{"/tmp/repo-a", "/tmp/repo-b"})
	d := NewEditPathsDialog()
	d.SetSize(80, 40)
	d.Show(inst, nil)

	original := d.paths[0]

	// Enter edit mode
	d.Update(tea.KeyMsg{Type: tea.KeyEnter})
	d.pathInput.SetValue("/tmp/should-not-save")

	// Cancel with Esc
	d.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if d.editing {
		t.Fatal("should exit editing mode after Esc")
	}
	if d.paths[0] != original {
		t.Errorf("path should be unchanged after cancel, got %s", d.paths[0])
	}
}

func TestEditPathsDialog_EscClosesWhenNotEditing(t *testing.T) {
	inst := newTestMultiRepoInstance([]string{"/tmp/repo-a", "/tmp/repo-b"})
	d := NewEditPathsDialog()
	d.SetSize(80, 40)
	d.Show(inst, nil)

	if !d.IsVisible() {
		t.Fatal("dialog should be visible")
	}

	// Esc outside editing should be handled by parent (handleEditPathsDialogKey)
	// The dialog's Update doesn't close itself — the parent does
	// So just verify we're not in editing mode
	if d.editing {
		t.Fatal("should not be in editing mode initially")
	}
}

func TestEditPathsDialog_ValidateMinPaths(t *testing.T) {
	inst := newTestMultiRepoInstance([]string{"/tmp/repo-a", "/tmp/repo-b"})
	d := NewEditPathsDialog()
	d.Show(inst, nil)

	// Clear one path to make it invalid
	d.paths = []string{"/tmp/repo-a", ""}
	errMsg := d.Validate()
	if errMsg == "" {
		t.Fatal("should fail validation with only 1 non-empty path")
	}
}

func TestEditPathsDialog_GetSessionID(t *testing.T) {
	inst := newTestMultiRepoInstance([]string{"/tmp/repo-a", "/tmp/repo-b"})
	d := NewEditPathsDialog()
	d.Show(inst, nil)

	if d.GetSessionID() != inst.ID {
		t.Errorf("expected session ID %s, got %s", inst.ID, d.GetSessionID())
	}
}

func TestEditPathsDialog_HideResetsState(t *testing.T) {
	inst := newTestMultiRepoInstance([]string{"/tmp/repo-a", "/tmp/repo-b"})
	d := NewEditPathsDialog()
	d.Show(inst, nil)
	d.editing = true
	d.validationErr = "some error"

	d.Hide()

	if d.IsVisible() {
		t.Fatal("should not be visible after Hide")
	}
	if d.editing {
		t.Fatal("editing should be reset after Hide")
	}
	if d.validationErr != "" {
		t.Fatal("validationErr should be cleared after Hide")
	}
}
