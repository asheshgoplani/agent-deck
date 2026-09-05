package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Regression tests for the sidebar viewport chasing the cursor: with the list
// scrolled to the bottom, pressing up dragged the whole window up with the
// selection instead of moving the selection within a stationary window. The
// scroll-back loop in syncViewport measured [viewOffset, cursor] and so pinned
// the cursor to the bottom edge; it must measure to the end of the list and
// only reclaim blank space left by removals or resizes.

// newScrollFixtureHome builds a Home whose sidebar rows are all exactly one
// line (classic layout) so viewport arithmetic is deterministic.
func newScrollFixtureHome(t *testing.T, itemCount int) *Home {
	t.Helper()
	home := NewHome()
	home.width = 80
	home.height = 12
	home.embeddedLayout = false
	items := make([]session.Item, itemCount)
	for i := range items {
		items[i] = session.Item{Type: session.ItemTypeSession}
	}
	home.flatItems = items
	return home
}

func TestSyncViewportHoldsStillWhileCursorMovesUp(t *testing.T) {
	const itemCount = 20
	home := newScrollFixtureHome(t, itemCount)

	home.cursor = itemCount - 1
	home.viewOffset = 0
	home.syncViewport()
	bottomOffset := home.viewOffset
	if bottomOffset == 0 {
		t.Fatal("fixture does not overflow the viewport; shrink height or add items")
	}

	// Moving up inside the window must not move the window.
	for cursor := itemCount - 2; cursor >= bottomOffset; cursor-- {
		home.cursor = cursor
		home.syncViewport()
		if home.viewOffset != bottomOffset {
			t.Fatalf("cursor=%d: viewOffset=%d, want %d (viewport must hold still while the cursor moves within it)",
				cursor, home.viewOffset, bottomOffset)
		}
	}

	// Crossing the top edge scrolls exactly one row.
	home.cursor = bottomOffset - 1
	home.syncViewport()
	if home.viewOffset != bottomOffset-1 {
		t.Fatalf("cursor above window: viewOffset=%d, want %d", home.viewOffset, bottomOffset-1)
	}
}

func TestSyncViewportReclaimsBlankSpaceAfterRemovals(t *testing.T) {
	const itemCount = 20
	const removed = 5
	home := newScrollFixtureHome(t, itemCount)

	home.cursor = itemCount - 1
	home.viewOffset = 0
	home.syncViewport()
	bottomOffset := home.viewOffset
	if bottomOffset <= removed {
		t.Fatalf("fixture too short for truncation test: viewOffset=%d", bottomOffset)
	}

	// Removing trailing items leaves blank space below the list; the viewport
	// must scroll back just enough to fill it and no further.
	home.flatItems = home.flatItems[:itemCount-removed]
	home.cursor = itemCount - removed - 1
	home.syncViewport()
	if want := bottomOffset - removed; home.viewOffset != want {
		t.Fatalf("after removing %d tail items: viewOffset=%d, want %d", removed, home.viewOffset, want)
	}
}

// The viewport arithmetic must hold when rows are not all one line: remote
// rows in the embedded layout are two-line cards at compact density, local
// rows too, and a group header is one line.
func TestSyncViewportHoldsStillWithRemoteRowsInEmbeddedLayout(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 20
	home.embeddedLayout = true
	items := []session.Item{{Type: session.ItemTypeRemoteGroup, RemoteName: "lab", Level: 0}}
	for i := 0; i < 12; i++ {
		items = append(items, session.Item{
			Type:          session.ItemTypeRemoteSession,
			RemoteSession: &session.RemoteSessionInfo{ID: "r", Title: "remote", Status: "running", Tool: "claude"},
			RemoteName:    "lab",
			Level:         1,
		})
	}
	home.flatItems = items
	if h := home.sidebarItemRenderHeightAtWidth(items[1], home.sessionsPaneWidth()); h != 2 {
		t.Fatalf("fixture remote row height = %d, want 2", h)
	}

	home.cursor = len(items) - 1
	home.viewOffset = 0
	home.syncViewport()
	bottomOffset := home.viewOffset
	if bottomOffset == 0 {
		t.Fatal("fixture does not overflow the viewport; shrink height or add items")
	}
	for cursor := len(items) - 2; cursor >= bottomOffset; cursor-- {
		home.cursor = cursor
		home.syncViewport()
		if home.viewOffset != bottomOffset {
			t.Fatalf("cursor=%d: viewOffset=%d, want %d (viewport must hold still over remote rows)",
				cursor, home.viewOffset, bottomOffset)
		}
	}
}
