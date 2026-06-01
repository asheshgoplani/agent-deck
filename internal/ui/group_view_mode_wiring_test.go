package ui

// Wiring tests for the group view-mode partition (running-on-top / populated-on-top).
// These exercise the real Home.rebuildFlatItems integration, the `t` keypress
// dispatch, and divider-skip navigation — beyond the pure PartitionByViewMode
// unit tests in the session package.

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func dividerIndex(h *Home) int {
	for i, it := range h.flatItems {
		if it.Type == session.ItemTypeDivider {
			return i
		}
	}
	return -1
}

func sessionIndexByTitle(h *Home, title string) int {
	for i, it := range h.flatItems {
		if it.Type == session.ItemTypeSession && it.Session != nil && it.Session.Title == title {
			return i
		}
	}
	return -1
}

func TestActiveTopWiringSplitsList(t *testing.T) {
	home, _ := buildTwoGroupHome(t)

	// a1 is the only active session; everything else is inactive.
	home.instancesMu.RLock()
	for _, inst := range home.instances {
		if inst.Title == "a1" {
			inst.Status = session.StatusRunning
		} else {
			inst.Status = session.StatusIdle
		}
	}
	home.instancesMu.RUnlock()

	home.groupViewMode = session.GroupViewActiveTop
	home.rebuildFlatItems()

	div := dividerIndex(home)
	if div < 0 {
		t.Fatalf("expected a divider when active and inactive sessions coexist; flatItems=%d", len(home.flatItems))
	}
	a1 := sessionIndexByTitle(home, "a1")
	if a1 < 0 || a1 >= div {
		t.Fatalf("active session a1 must be above the divider: a1=%d divider=%d", a1, div)
	}
	for _, title := range []string{"a2", "a3", "b1", "b2"} {
		if idx := sessionIndexByTitle(home, title); idx < div {
			t.Fatalf("inactive session %q must be below the divider: idx=%d divider=%d", title, idx, div)
		}
	}
}

func TestPopulatedTopWiringSinksEmptyGroup(t *testing.T) {
	home, _ := buildTwoGroupHome(t)

	// Add an empty group with no sessions.
	home.groupTree.CreateGroup("empties")
	home.groupViewMode = session.GroupViewPopulatedTop
	home.rebuildFlatItems()

	div := dividerIndex(home)
	if div < 0 {
		t.Fatalf("expected a divider when an empty group exists alongside populated ones")
	}
	// All sessions must be above the divider (populated mode never splits sessions).
	for _, title := range []string{"a1", "a2", "a3", "b1", "b2"} {
		if idx := sessionIndexByTitle(home, title); idx < 0 || idx >= div {
			t.Fatalf("session %q must be above the divider in populated mode: idx=%d divider=%d", title, idx, div)
		}
	}
	// The empty group header must be below the divider.
	emptyBelow := false
	for i := div + 1; i < len(home.flatItems); i++ {
		if home.flatItems[i].Type == session.ItemTypeGroup && home.flatItems[i].Path == "empties" {
			emptyBelow = true
		}
	}
	if !emptyBelow {
		t.Fatal("empty group 'empties' must appear below the divider")
	}
}

func TestActiveTopWiringCollapsedRunningGroupStaysTop(t *testing.T) {
	home, _ := buildTwoGroupHome(t)

	// alpha holds a running session but is COLLAPSED (its session rows are not
	// flattened). beta is expanded and idle. Regression guard: a collapsed group
	// with running work must NOT sink below the "idle / done" divider.
	home.instancesMu.RLock()
	for _, inst := range home.instances {
		if inst.Title == "a1" {
			inst.Status = session.StatusRunning
		} else {
			inst.Status = session.StatusIdle
		}
	}
	home.instancesMu.RUnlock()
	home.groupTree.Groups["alpha"].Expanded = false

	home.groupViewMode = session.GroupViewActiveTop
	home.rebuildFlatItems()

	div := dividerIndex(home)
	if div < 0 {
		t.Fatalf("expected a divider; flatItems=%d", len(home.flatItems))
	}
	alphaIdx := -1
	for i, it := range home.flatItems {
		if it.Type == session.ItemTypeGroup && it.Path == "alpha" {
			alphaIdx = i
		}
	}
	if alphaIdx < 0 {
		t.Fatal("collapsed alpha header missing from flatItems")
	}
	if alphaIdx >= div {
		t.Fatalf("collapsed running group 'alpha' must stay ABOVE the divider: alpha=%d divider=%d", alphaIdx, div)
	}
}

func TestCycleGroupViewKeyTogglesMode(t *testing.T) {
	home, _ := buildTwoGroupHome(t)
	if home.groupViewMode != session.GroupViewNormal {
		t.Fatalf("expected initial mode Normal, got %v", home.groupViewMode)
	}
	press := func() {
		home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	}
	press()
	if home.groupViewMode != session.GroupViewActiveTop {
		t.Fatalf("after 1 press expected ActiveTop, got %v", home.groupViewMode)
	}
	press()
	if home.groupViewMode != session.GroupViewPopulatedTop {
		t.Fatalf("after 2 presses expected PopulatedTop, got %v", home.groupViewMode)
	}
	press()
	if home.groupViewMode != session.GroupViewNormal {
		t.Fatalf("after 3 presses expected Normal again, got %v", home.groupViewMode)
	}
}

func TestSkipDividerNavigationGlidesPast(t *testing.T) {
	home, _ := buildTwoGroupHome(t)
	home.instancesMu.RLock()
	for _, inst := range home.instances {
		if inst.Title == "a1" {
			inst.Status = session.StatusRunning
		} else {
			inst.Status = session.StatusIdle
		}
	}
	home.instancesMu.RUnlock()
	home.groupViewMode = session.GroupViewActiveTop
	home.rebuildFlatItems()

	div := dividerIndex(home)
	if div <= 0 || div >= len(home.flatItems)-1 {
		t.Fatalf("divider should be interior, got %d of %d", div, len(home.flatItems))
	}

	// Cursor immediately above the divider; pressing down must skip onto div+1.
	home.cursor = div - 1
	home.handleMainKey(tea.KeyMsg{Type: tea.KeyDown})
	if home.cursor != div+1 {
		t.Fatalf("down across divider: cursor=%d, want %d (skipping divider at %d)", home.cursor, div+1, div)
	}
	if home.flatItems[home.cursor].Type == session.ItemTypeDivider {
		t.Fatal("cursor landed on a divider after navigating down")
	}

	// And back up must skip onto div-1.
	home.handleMainKey(tea.KeyMsg{Type: tea.KeyUp})
	if home.cursor != div-1 {
		t.Fatalf("up across divider: cursor=%d, want %d", home.cursor, div-1)
	}
}
