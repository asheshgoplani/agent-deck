package ui

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// addCreatingPlaceholder registers a creating-session placeholder and rebuilds
// the flat list, returning the placeholder's flatItems index.
func addCreatingPlaceholder(t *testing.T, home *Home, tempID, groupPath string) int {
	t.Helper()
	home.creatingSessions[tempID] = &CreatingSession{
		ID:        tempID,
		Title:     "wt-new",
		Tool:      "claude",
		GroupPath: groupPath,
		StartTime: time.Now(),
	}
	home.rebuildFlatItems()
	for i, it := range home.flatItems {
		if it.CreatingID == tempID {
			return i
		}
	}
	t.Fatalf("placeholder %q not injected into flatItems", tempID)
	return -1
}

// Selection must stay on the creating placeholder across a preserve/rebuild
// cycle even when rows are inserted above it (a storage reload adding a
// session, a status repartition, etc.). Placeholders have no Session, so the
// identity must be carried via CreatingID rather than falling back to the
// numeric clamp.
func TestPreserveSelection_CreatingPlaceholder_SurvivesIndexShift(t *testing.T) {
	home, _ := buildTwoGroupHome(t)
	home.cursor = addCreatingPlaceholder(t, home, "temp-123", "beta")

	identity := home.captureSelectedItemIdentity()
	if identity.creatingID != "temp-123" {
		t.Fatalf("captured identity missing creatingID: %+v", identity)
	}

	// Shift indices: add a session to a group sorted above the placeholder.
	extra := session.NewInstanceWithTool("a0", "/tmp/a0", "claude")
	extra.GroupPath = "alpha"
	home.instancesMu.Lock()
	home.instances = append(home.instances, extra)
	home.instancesMu.Unlock()
	home.groupTree.AddSession(extra)

	home.rebuildFlatItemsPreservingSelection(identity)

	if got := home.flatItems[home.cursor].CreatingID; got != "temp-123" {
		t.Fatalf("cursor left the placeholder after rebuild: cursor=%d item=%+v",
			home.cursor, home.flatItems[home.cursor])
	}
}

// restoreState (the storage-reload path) must restore the cursor onto the
// placeholder via cursorCreatingID.
func TestRestoreState_CreatingPlaceholder_Restored(t *testing.T) {
	home, _ := buildTwoGroupHome(t)
	home.cursor = addCreatingPlaceholder(t, home, "temp-456", "beta")

	state := home.preserveState()
	if state.cursorCreatingID != "temp-456" {
		t.Fatalf("preserveState missing cursorCreatingID: %+v", state)
	}

	// Shift indices before restore, mimicking a reload that loaded a new session.
	extra := session.NewInstanceWithTool("a0", "/tmp/a0", "claude")
	extra.GroupPath = "alpha"
	home.instancesMu.Lock()
	home.instances = append(home.instances, extra)
	home.instancesMu.Unlock()
	home.groupTree.AddSession(extra)
	home.cursor = 0

	home.restoreState(state)

	if got := home.flatItems[home.cursor].CreatingID; got != "temp-456" {
		t.Fatalf("cursor not restored to placeholder: cursor=%d item=%+v",
			home.cursor, home.flatItems[home.cursor])
	}
}

// When the placeholder is gone by restore time (creation completed), the
// restore must fall back cleanly without panicking or landing out of range.
func TestRestoreState_CreatingPlaceholderGone_FallsBack(t *testing.T) {
	home, _ := buildTwoGroupHome(t)
	home.cursor = addCreatingPlaceholder(t, home, "temp-789", "beta")

	state := home.preserveState()

	delete(home.creatingSessions, "temp-789")
	home.restoreState(state)

	if home.cursor < 0 || home.cursor >= len(home.flatItems) {
		t.Fatalf("cursor out of range after fallback: %d of %d", home.cursor, len(home.flatItems))
	}
	if home.flatItems[home.cursor].Type == session.ItemTypeDivider {
		t.Fatalf("cursor must not rest on a divider after fallback: cursor=%d", home.cursor)
	}
}
