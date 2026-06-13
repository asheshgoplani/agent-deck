package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Regression: the preview pane must not panic when the cursor lands on a
// non-selectable divider row (ItemTypeDivider carries a nil Session). Dividers
// are injected by the view-mode partition feature and the cursor can come to
// rest on one after a state-restore/clamp (e.g. when the previously-selected
// session is now hidden by the archive filter), before any j/k navigation runs
// skipDivider. renderPreviewPane previously fell through every type check to
// `selected := item.Session` and dereferenced nil. See startup panic at
// home.go:13362.
func TestPreviewPane_DividerAtCursor_NoPanic(t *testing.T) {
	h := NewHome()
	h.width = 120
	h.height = 40
	h.initialLoading = false

	h.flatItems = []session.Item{
		{Type: session.ItemTypeDivider, DividerLabel: "idle / done"},
	}
	h.cursor = 0
	h.setHotkeys(resolveHotkeys(nil))

	// Must not panic.
	_ = h.renderPreviewPane(80, 30)
}

// Regression: when the cursor-restore fallback clamps onto a divider (the
// previously-selected session is gone — e.g. archive-hidden — so no session/
// group match, and a divider exists because a non-Normal view mode is active),
// restoreState must nudge the cursor onto a real selectable row rather than
// leaving it parked on the non-selectable divider. This both exercises the
// skipDivider(1) call and reproduces the startup condition that triggered the
// home.go:13362 panic.
func TestRestoreState_FallbackOntoDivider_LandsOnSession(t *testing.T) {
	home, _ := buildTwoGroupHome(t)

	// One running session + the rest idle so ActiveTop splits the list with a
	// divider between the active and idle sections.
	setOnlySessionRunning(t, home, "a1")
	home.groupViewMode = session.GroupViewActiveTop
	home.rebuildFlatItems()

	div := dividerIndex(home)
	if div < 0 {
		t.Fatalf("expected a divider in ActiveTop mode; flatItems=%d", len(home.flatItems))
	}

	// Park the cursor on the divider, then restore with a session ID that no
	// longer exists (mimicking an archived/removed session). The fallback clamp
	// keeps the cursor at the divider index; skipDivider(1) must move it off.
	home.cursor = div
	home.restoreState(reloadState{
		cursorSessionID: "no-such-session-id",
		expandedGroups:  map[string]bool{},
	})

	if home.cursor < 0 || home.cursor >= len(home.flatItems) {
		t.Fatalf("cursor out of range after restore: %d of %d", home.cursor, len(home.flatItems))
	}
	if home.flatItems[home.cursor].Type == session.ItemTypeDivider {
		t.Fatalf("cursor must not rest on a divider after restore: cursor=%d", home.cursor)
	}
	if home.flatItems[home.cursor].Type == session.ItemTypeSession &&
		home.flatItems[home.cursor].Session == nil {
		t.Fatalf("cursor rests on a session row with nil Session: cursor=%d", home.cursor)
	}
}

func TestRestoreState_RemoteSessionNotApplicable(t *testing.T) {
	t.Skip("RemoteSession N/A: restoreState persists local session/group IDs only; remote rows are rebuilt from live SSH fetches and covered by t-cycle selection preservation.")
}
