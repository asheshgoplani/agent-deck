package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestGroupDialogReopensAfterClose is a regression guard for the gg-detection
// collision: pressing the create-group key ('g') arms the Vi-style "gg"
// double-tap timer. After the dialog closes, a second 'g' pressed within the
// 500ms window used to be swallowed as a gg-jump-to-top instead of reopening
// the dialog — so the group appeared to need a "second try" to create.
//
// The two key presses in this test are microseconds apart (well inside the
// 500ms window), so without the lastGTime reset on close the second press
// jumps to top and the dialog stays hidden.
func TestGroupDialogReopensAfterClose(t *testing.T) {
	pressG := func(h *Home) *Home {
		model, _ := h.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
		h2, ok := model.(*Home)
		if !ok {
			t.Fatal("handleMainKey should return *Home")
		}
		return h2
	}

	for _, tc := range []struct {
		name  string
		close func(h *Home) *Home
	}{
		{
			name: "after esc cancel",
			close: func(h *Home) *Home {
				model, _ := h.handleGroupDialogKey(tea.KeyMsg{Type: tea.KeyEsc})
				return model.(*Home)
			},
		},
		{
			name: "after enter create",
			close: func(h *Home) *Home {
				h.groupDialog.nameInput.SetValue("alpha")
				model, _ := h.handleGroupDialogKey(tea.KeyMsg{Type: tea.KeyEnter})
				return model.(*Home)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHome()
			h.width, h.height = 100, 30
			h.groupTree = session.NewGroupTree([]*session.Instance{})
			h.rebuildFlatItems()

			// First 'g' opens the dialog and arms the gg timer.
			h = pressG(h)
			if !h.groupDialog.IsVisible() {
				t.Fatal("first 'g' should open the group dialog")
			}

			// Close it (esc or enter).
			h = tc.close(h)
			if h.groupDialog.IsVisible() {
				t.Fatal("dialog should be hidden after close")
			}

			// Second 'g', microseconds later, must reopen the dialog — NOT be
			// eaten as a gg-jump-to-top.
			h = pressG(h)
			if !h.groupDialog.IsVisible() {
				t.Error("second 'g' right after close should reopen the dialog, not jump to top")
			}
		})
	}
}
