package ui

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestPlaceholderMutationGuards verifies that move/fork/rename over a
// still-creating placeholder row (Type==ItemTypeSession, Session==nil) neither
// panics (#1540) nor silently no-ops: each surfaces errSessionStillCreating.
// Without the IsCreatingPlaceholder guards the move path would deref a nil
// *Instance and panic; fork/rename would silently do nothing.
func TestPlaceholderMutationGuards(t *testing.T) {
	for _, key := range []rune{'M', 'f', 'F', 'r'} {
		t.Run(string(key), func(t *testing.T) {
			home := NewHome()
			home.width = 100
			home.height = 30
			home.flatItems = []session.Item{{
				Type:          session.ItemTypeSession,
				Session:       nil, // placeholder: not yet created
				CreatingID:    "tmp-guard",
				CreatingTitle: "creating…",
			}}
			home.cursor = 0

			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("pressing %q on a placeholder panicked: %v", key, r)
				}
			}()

			model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
			h, ok := model.(*Home)
			if !ok {
				t.Fatalf("handleMainKey did not return *Home")
			}
			if !errors.Is(h.err, errSessionStillCreating) {
				t.Fatalf("key %q on placeholder: h.err = %v, want errSessionStillCreating", key, h.err)
			}
		})
	}
}
