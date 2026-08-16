// Remote group headers rendered a ▾ marker but were not wired to any fold
// state: buildRemoteFlatItems emitted the whole subtree unconditionally and
// collapseOrNavUp had no ItemTypeRemoteGroup branch, so h/left/Enter/Tab did
// nothing on a remote row. These tests pin the collapse behavior.

package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// collapseTestSessions is a remote with a nested tree:
//
//	remotes/dev/my-sessions   -> loose
//	remotes/dev/work          -> api-1
//	remotes/dev/work/api      -> api-2
func collapseTestSessions() []session.RemoteSessionInfo {
	return []session.RemoteSessionInfo{
		{ID: "a", Title: "api-1", Group: "work", Status: "running"},
		{ID: "b", Title: "api-2", Group: "work/api", Status: "idle"},
		{ID: "c", Title: "loose", Group: "", Status: "waiting"},
	}
}

// newCollapseTestHome returns a Home wired to the collapse fixture with a known
// fold state. The ui package shares one temp profile DB across the whole test
// binary (testmain_test.go), so a Home that persists UI state would inherit the
// folds an earlier test saved — detaching storage keeps each case hermetic.
func newCollapseTestHome() *Home {
	h := NewHome()
	h.storage = nil
	h.remoteGroupsCollapsed = make(map[string]bool)
	h.remoteSessions = map[string][]session.RemoteSessionInfo{
		"dev": collapseTestSessions(),
	}
	h.rebuildFlatItems()
	return h
}

func pathsByType(items []session.Item, typ session.ItemType) map[string]bool {
	out := map[string]bool{}
	for _, it := range items {
		if it.Type == typ {
			out[it.Path] = true
		}
	}
	return out
}

// A collapsed Level-0 header keeps its own row — otherwise the user could
// never reopen it — but withholds every group and session below it.
func TestRemoteCollapse_RootHidesWholeSubtree(t *testing.T) {
	items := buildRemoteFlatItems("dev", collapseTestSessions(), map[string]bool{
		"remotes/dev": true,
	})

	if len(items) != 1 {
		t.Fatalf("collapsed remote must emit only its header, got %d items", len(items))
	}
	if items[0].Type != session.ItemTypeRemoteGroup || items[0].Path != "remotes/dev" {
		t.Fatalf("surviving row must be the Level-0 header, got %+v", items[0])
	}
}

// Collapsing an intermediate group hides its sessions and its descendant
// headers, while its siblings stay untouched.
func TestRemoteCollapse_SubGroupHidesDescendantsOnly(t *testing.T) {
	items := buildRemoteFlatItems("dev", collapseTestSessions(), map[string]bool{
		"remotes/dev/work": true,
	})

	headers := pathsByType(items, session.ItemTypeRemoteGroup)
	if !headers["remotes/dev/work"] {
		t.Error("the collapsed header itself must remain visible")
	}
	if headers["remotes/dev/work/api"] {
		t.Error("descendant header of a collapsed group must be hidden")
	}
	if !headers["remotes/dev/my-sessions"] {
		t.Error("sibling group must be unaffected by the collapse")
	}

	visible := map[string]bool{}
	for _, it := range items {
		if it.Type == session.ItemTypeRemoteSession {
			visible[it.RemoteSession.ID] = true
		}
	}
	if visible["a"] {
		t.Error("session directly in the collapsed group must be hidden")
	}
	if visible["b"] {
		t.Error("session in a descendant of the collapsed group must be hidden")
	}
	if !visible["c"] {
		t.Error("session in a sibling group must stay visible")
	}
}

// A nil map is the pre-collapse contract: emit everything.
func TestRemoteCollapse_NilMapEmitsFullTree(t *testing.T) {
	full := buildRemoteFlatItems("dev", collapseTestSessions(), nil)
	empty := buildRemoteFlatItems("dev", collapseTestSessions(), map[string]bool{})

	if len(full) != len(empty) {
		t.Fatalf("nil and empty collapse maps must agree: %d vs %d", len(full), len(empty))
	}
	if len(full) != 7 { // 1 root + 3 group headers + 3 sessions
		t.Fatalf("expected the full 7-row tree, got %d", len(full))
	}
}

// Sorting emits "work" before "work/api", so the collapse flag for "work" is
// recorded while walking one path and must still suppress the deeper header
// when the walk reaches the sibling path that would have emitted it.
func TestRemoteCollapse_AncestorFlagSurvivesPrefixWalk(t *testing.T) {
	sessions := []session.RemoteSessionInfo{
		{ID: "deep", Title: "deep", Group: "work/api/v2", Status: "idle"},
	}

	items := buildRemoteFlatItems("dev", sessions, map[string]bool{
		"remotes/dev/work": true,
	})

	headers := pathsByType(items, session.ItemTypeRemoteGroup)
	for _, hidden := range []string{"remotes/dev/work/api", "remotes/dev/work/api/v2"} {
		if headers[hidden] {
			t.Errorf("%s must stay hidden under the collapsed ancestor", hidden)
		}
	}
	if len(pathsByType(items, session.ItemTypeRemoteSession)) != 0 {
		t.Error("no session may surface under a collapsed ancestor")
	}
}

// h/left on a remote header folds it; pressing again walks to the parent
// instead of repainting, mirroring the local collapse-or-nav-up contract.
func TestRemoteCollapse_CollapseOrNavUpFoldsThenWalksUp(t *testing.T) {
	h := newCollapseTestHome()

	target := "remotes/dev/work"
	h.moveCursorToRemoteGroup("dev", target)
	if h.flatItems[h.cursor].Path != target {
		t.Fatalf("cursor setup failed, sits on %q", h.flatItems[h.cursor].Path)
	}

	h.collapseOrNavUp()
	if !h.isRemoteGroupCollapsed(target) {
		t.Fatal("first press must collapse the header")
	}
	if got := h.flatItems[h.cursor].Path; got != target {
		t.Fatalf("cursor must stay on the collapsed header, sits on %q", got)
	}

	h.collapseOrNavUp()
	if got := h.flatItems[h.cursor].Path; got != "remotes/dev" {
		t.Fatalf("second press must walk up to the remote root, sits on %q", got)
	}
	if !h.isRemoteGroupCollapsed(target) {
		t.Error("walking up must not reopen the header")
	}
}

// Toggling is symmetric and leaves no stale key behind.
func TestRemoteCollapse_ToggleRoundTrip(t *testing.T) {
	h := newCollapseTestHome()

	h.toggleRemoteGroup("dev", "remotes/dev/work")
	if !h.isRemoteGroupCollapsed("remotes/dev/work") {
		t.Fatal("toggle must collapse an open header")
	}

	h.toggleRemoteGroup("dev", "remotes/dev/work")
	if h.isRemoteGroupCollapsed("remotes/dev/work") {
		t.Fatal("toggle must reopen a collapsed header")
	}
	if _, stale := h.remoteGroupsCollapsed["remotes/dev/work"]; stale {
		t.Error("reopening must delete the key, not store false")
	}
}

func TestRemoteGroupParentPath(t *testing.T) {
	cases := map[string]string{
		"remotes/dev/work/api": "remotes/dev/work",
		"remotes/dev/work":     "remotes/dev",
		"remotes/dev":          "",
		"remotes":              "",
	}
	for in, want := range cases {
		if got := remoteGroupParentPath(in); got != want {
			t.Errorf("remoteGroupParentPath(%q) = %q, want %q", in, got, want)
		}
	}
}
