package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

func TestStatusFilterKeysPreserveSelectedSession(t *testing.T) {
	home, instances := buildFocusHome(t)
	home.embeddedLayout = true
	instances[0].Status = session.StatusRunning
	instances[1].Status = session.StatusWaiting
	instances[2].Status = session.StatusWaiting
	instances[3].Status = session.StatusIdle
	home.rebuildFlatItems()

	press := func(key rune) {
		t.Helper()
		home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
	}
	selectedID := instances[2].ID // Waiting session in the second group.
	press('@')
	home.cursor = home.flatItemIndexByID(selectedID)
	if home.cursor < 0 {
		t.Fatal("waiting session is missing from the filtered list")
	}
	filteredIndex := home.cursor
	press('0')
	if got := home.flatItemIndexByID(selectedID); got == filteredIndex {
		t.Fatal("fixture did not move the session's row when clearing the filter")
	} else if home.cursor != got {
		t.Fatalf("clear filter selected row %d, want session %s at row %d", home.cursor, selectedID, got)
	}

	// When the selected session is excluded, keep the existing clamp fallback.
	press('!')
	if got := home.flatItemIndexByID(selectedID); got != -1 {
		t.Fatalf("waiting session remained in running filter at row %d", got)
	}
	if want := len(home.flatItems) - 1; home.cursor != want {
		t.Fatalf("filtered-out selection moved to row %d, want clamped row %d", home.cursor, want)
	}
}
