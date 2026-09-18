package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestHelpOverlayDoesNotAdvertiseUnreachableGGChord: the help overlay used to
// document a "gg" jump-to-top chord that a single-key 'g' binding (New group)
// could never reach, paired with 'G' even though 'G' only opens global search.
func TestHelpOverlayDoesNotAdvertiseUnreachableGGChord(t *testing.T) {
	h := NewHelpOverlay()
	h.Show()
	h.SetSize(200, 60)
	view := h.View()

	if strings.Contains(view, "gg / G") || strings.Contains(view, "Jump to top / global search") {
		t.Errorf("help overlay still advertises the unreachable 'gg' chord: %q", view)
	}
	if !strings.Contains(view, "Home / End") {
		t.Error("help overlay should document Home/End for jump to first/last item")
	}
}

// TestHomeNavigationKeysMatchHelpOverlay verifies the three keys the help
// overlay documents in the NAVIGATION section — Home (jump to first item),
// G (global search) and g (new group, GROUPS section) — actually do what
// the help text says, and that 'g' never behaves as a jump-to-top chord.
func TestHomeNavigationKeysMatchHelpOverlay(t *testing.T) {
	newHomeWithItems := func() *Home {
		h := NewHome()
		h.width, h.height = 100, 30
		h.storage = nil
		insts := []*session.Instance{
			{ID: "s1", Title: "s1", Tool: "claude", Status: session.StatusIdle, LastAccessedAt: time.Now()},
			{ID: "s2", Title: "s2", Tool: "claude", Status: session.StatusIdle, LastAccessedAt: time.Now()},
			{ID: "s3", Title: "s3", Tool: "claude", Status: session.StatusIdle, LastAccessedAt: time.Now()},
		}
		h.groupTree = session.NewGroupTree(insts)
		h.rebuildFlatItems()
		return h
	}

	t.Run("home jumps to first item", func(t *testing.T) {
		h := newHomeWithItems()
		h.cursor = len(h.flatItems) - 1
		model, _ := h.handleMainKey(tea.KeyMsg{Type: tea.KeyHome})
		h2 := model.(*Home)
		if h2.cursor != 0 {
			t.Errorf("cursor after 'home' = %d, want 0", h2.cursor)
		}
	})

	t.Run("G opens global search (or local search fallback)", func(t *testing.T) {
		h := newHomeWithItems()
		model, _ := h.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
		h2 := model.(*Home)
		if !h2.globalSearch.IsVisible() && !h2.search.IsVisible() {
			t.Error("'G' should open global search or fall back to local search")
		}
	})

	t.Run("g always opens New group, never a gg jump-to-top", func(t *testing.T) {
		h := newHomeWithItems()
		h.cursor = len(h.flatItems) - 1
		before := h.cursor

		model, _ := h.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
		h2 := model.(*Home)
		if !h2.groupDialog.IsVisible() {
			t.Fatal("first 'g' should open the create-group dialog")
		}
		if h2.cursor != before {
			t.Errorf("'g' should never move the cursor (no jump-to-top side effect); cursor = %d, want %d", h2.cursor, before)
		}

		h2.groupDialog.Hide()
		model, _ = h2.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
		h3 := model.(*Home)
		if !h3.groupDialog.IsVisible() {
			t.Error("a second 'g' should also open the create-group dialog, not jump to top")
		}
		if h3.cursor != before {
			t.Errorf("second 'g' should never move the cursor; cursor = %d, want %d", h3.cursor, before)
		}
	})
}
