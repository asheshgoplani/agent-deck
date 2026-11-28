package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// remoteSelectionHome builds a Home showing two remotes: "box" with four
// sessions in one group, and "zed" below it with one.
func remoteSelectionHome(t *testing.T) *Home {
	t.Helper()
	home := newTestHomeWithItems(100, 30, nil)
	home.remoteSessions = map[string][]session.RemoteSessionInfo{
		"box": {
			{ID: "a", Title: "alpha", Group: "work", RemoteName: "box"},
			{ID: "b", Title: "bravo", Group: "work", RemoteName: "box"},
			{ID: "c", Title: "charlie", Group: "work", RemoteName: "box"},
			{ID: "d", Title: "delta", Group: "work", RemoteName: "box"},
		},
		"zed": {
			{ID: "z", Title: "zulu", Group: "work", RemoteName: "zed"},
		},
	}
	home.rebuildFlatItems()
	return home
}

func selectRemoteRow(t *testing.T, h *Home, remote, id string) {
	t.Helper()
	for i, item := range h.flatItems {
		if item.Type == session.ItemTypeRemoteSession && item.RemoteSession != nil &&
			item.RemoteName == remote && item.RemoteSession.ID == id {
			h.cursor = i
			return
		}
	}
	t.Fatalf("remote row %s/%s is not in the list", remote, id)
}

func requireCursorOnRemote(t *testing.T, h *Home, remote, id string) {
	t.Helper()
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		t.Fatalf("cursor %d is outside the %d rows", h.cursor, len(h.flatItems))
	}
	item := h.flatItems[h.cursor]
	if item.Type != session.ItemTypeRemoteSession || item.RemoteSession == nil ||
		item.RemoteName != remote || item.RemoteSession.ID != id {
		got := item.Path
		if item.RemoteSession != nil {
			got = item.RemoteName + "/" + item.RemoteSession.ID
		}
		t.Fatalf("cursor is on %q (type %v), want remote session %s/%s", got, item.Type, remote, id)
	}
}

// A fetch that no longer lists a remote session above the cursor must leave
// the cursor on the session it was on, not on the row that took its index.
func TestRemoteFetch_RemovalAboveKeepsCursorOnSameSession(t *testing.T) {
	home := remoteSelectionHome(t)
	selectRemoteRow(t, home, "box", "c")

	home.applyRemoteFetch(remoteSessionsFetchedMsg{sessions: map[string][]session.RemoteSessionInfo{
		"box": {
			{ID: "b", Title: "bravo", Group: "work", RemoteName: "box"},
			{ID: "c", Title: "charlie", Group: "work", RemoteName: "box"},
			{ID: "d", Title: "delta", Group: "work", RemoteName: "box"},
		},
	}})

	requireCursorOnRemote(t, home, "box", "c")
}

// A fetch that lists a new remote session above the cursor must not push the
// cursor onto the previous session.
func TestRemoteFetch_AdditionAboveKeepsCursorOnSameSession(t *testing.T) {
	home := remoteSelectionHome(t)
	selectRemoteRow(t, home, "zed", "z")

	home.applyRemoteFetch(remoteSessionsFetchedMsg{sessions: map[string][]session.RemoteSessionInfo{
		"box": {
			{ID: "a", Title: "alpha", Group: "work", RemoteName: "box"},
			{ID: "b", Title: "bravo", Group: "work", RemoteName: "box"},
			{ID: "c", Title: "charlie", Group: "work", RemoteName: "box"},
			{ID: "d", Title: "delta", Group: "work", RemoteName: "box"},
			{ID: "e", Title: "echo", Group: "work", RemoteName: "box"},
		},
		"zed": {
			{ID: "z", Title: "zulu", Group: "work", RemoteName: "zed"},
		},
	}})

	requireCursorOnRemote(t, home, "zed", "z")
}

// When the selected session itself is gone the cursor keeps its position and
// lands on the next session, as it did before.
func TestRemoteFetch_RemovingSelectedRowKeepsPosition(t *testing.T) {
	home := remoteSelectionHome(t)
	selectRemoteRow(t, home, "box", "c")
	before := home.cursor

	home.applyRemoteFetch(remoteSessionsFetchedMsg{sessions: map[string][]session.RemoteSessionInfo{
		"box": {
			{ID: "a", Title: "alpha", Group: "work", RemoteName: "box"},
			{ID: "b", Title: "bravo", Group: "work", RemoteName: "box"},
			{ID: "d", Title: "delta", Group: "work", RemoteName: "box"},
		},
	}})

	if home.cursor != before {
		t.Fatalf("cursor = %d, want it to stay at %d", home.cursor, before)
	}
	requireCursorOnRemote(t, home, "box", "d")
}

// A confirmed delete drops its row before any fetch. A cursor that has since
// moved to a session below must stay on that session.
func TestRemoteDrop_AboveKeepsCursorOnSameSession(t *testing.T) {
	home := remoteSelectionHome(t)
	selectRemoteRow(t, home, "box", "c")

	home.dropRemoteSession("box", "b")

	requireCursorOnRemote(t, home, "box", "c")
}

func TestRemoteDrop_SelectedRowKeepsPosition(t *testing.T) {
	home := remoteSelectionHome(t)
	selectRemoteRow(t, home, "box", "c")
	before := home.cursor

	home.dropRemoteSession("box", "c")

	if home.cursor != before {
		t.Fatalf("cursor = %d, want it to stay at %d", home.cursor, before)
	}
	requireCursorOnRemote(t, home, "box", "d")
}

// A session the remote just confirmed is inserted before any fetch.
func TestRemoteInsert_AboveKeepsCursorOnSameSession(t *testing.T) {
	home := remoteSelectionHome(t)
	selectRemoteRow(t, home, "zed", "z")

	home.insertRemoteSession(session.RemoteSessionInfo{ID: "e", Title: "echo", Group: "work", RemoteName: "box"})

	requireCursorOnRemote(t, home, "zed", "z")
}

// A patch can move a session to another group, which adds a group row.
func TestRemotePatch_NewGroupAboveKeepsCursorOnSameSession(t *testing.T) {
	home := remoteSelectionHome(t)
	selectRemoteRow(t, home, "zed", "z")

	home.patchRemoteSession("box", "d", func(s *session.RemoteSessionInfo) { s.Group = "other" })

	requireCursorOnRemote(t, home, "zed", "z")
}

// Creating and deleting a remote group adds and removes a group row.
func TestRemoteGroupCreateAndDelete_AboveKeepsCursorOnSameSession(t *testing.T) {
	home := remoteSelectionHome(t)
	selectRemoteRow(t, home, "zed", "z")
	rows := len(home.flatItems)

	home.Update(remoteGroupResultMsg{remoteName: "box", groupPath: "empty"})
	if len(home.flatItems) != rows+1 {
		t.Fatalf("rows after group create = %d, want %d", len(home.flatItems), rows+1)
	}
	requireCursorOnRemote(t, home, "zed", "z")

	home.Update(remoteGroupDeleteResultMsg{remoteName: "box", groupPath: "empty"})
	if len(home.flatItems) != rows {
		t.Fatalf("rows after group delete = %d, want %d", len(home.flatItems), rows)
	}
	requireCursorOnRemote(t, home, "zed", "z")
}
