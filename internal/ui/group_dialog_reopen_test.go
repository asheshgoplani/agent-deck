package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestGroupDialogReopensAfterClose is a regression guard: pressing the
// create-group key ('g') must always open the dialog immediately, and
// reopen it again right after a close, regardless of how quickly the two
// presses happen. 'g' has no double-tap ("gg") behavior — that chord was
// unreachable and was removed; jump-to-top lives on "home" instead.
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

			// First 'g' opens the dialog.
			h = pressG(h)
			if !h.groupDialog.IsVisible() {
				t.Fatal("first 'g' should open the group dialog")
			}

			// Close it (esc or enter).
			h = tc.close(h)
			if h.groupDialog.IsVisible() {
				t.Fatal("dialog should be hidden after close")
			}

			// Second 'g', microseconds later, must reopen the dialog.
			h = pressG(h)
			if !h.groupDialog.IsVisible() {
				t.Error("second 'g' right after close should reopen the dialog, not jump to top")
			}
		})
	}
}
