// Issue #1875: shift+up/down (and the +/-/K/J aliases) do nothing on a remote
// session row.
//
// The move handlers in home.go switch on ItemTypeGroup and ItemTypeSession
// only; ItemTypeRemoteSession and ItemTypeRemoteGroup fall off the end of the
// switch, so the keypress produces no repaint, no error and no state change.
// Because the same keys work on the local rows immediately above, it reads as
// a stuck key rather than an unimplemented path.
//
// These two tests drive the real handler through handleMainKey. They are
// written against the pre-fix API surface only, so they compile and FAIL at
// the merge base:
//
//   - the reorder test fails because the row order never changes;
//   - the never-silent test fails because h.err stays nil.
package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// armHomeForRemoteReorder builds a Home with one remote holding three sessions
// in a single group, in fetch order alpha, beta, gamma.
func armHomeForRemoteReorder(t *testing.T) *Home {
	t.Helper()

	withTempAgentDeckHome(t, `
[remotes.dev]
host = "user@dev.example"
agent_deck_path = "/usr/local/bin/agent-deck"
`)

	h := NewHome()
	h.width, h.height = 160, 40
	h.initialLoading = false
	h.remoteSessions = map[string][]session.RemoteSessionInfo{
		"dev": {
			{ID: "s-alpha", Title: "alpha", Status: "idle", Tool: "claude", Group: "work"},
			{ID: "s-beta", Title: "beta", Status: "idle", Tool: "claude", Group: "work"},
			{ID: "s-gamma", Title: "gamma", Status: "idle", Tool: "claude", Group: "work"},
		},
	}
	h.rebuildFlatItems()
	return h
}

// visibleRemoteSessionIDs reads the on-screen order of one remote's session
// rows straight out of the flat item list.
func visibleRemoteSessionIDs(h *Home, remoteName string) []string {
	var ids []string
	for _, it := range h.flatItems {
		if it.Type == session.ItemTypeRemoteSession && it.RemoteSession != nil && it.RemoteName == remoteName {
			ids = append(ids, it.RemoteSession.ID)
		}
	}
	return ids
}

// putCursorOnRemoteSession parks the cursor on a remote session row by ID.
func putCursorOnRemoteSession(t *testing.T, h *Home, id string) {
	t.Helper()
	for i, it := range h.flatItems {
		if it.Type == session.ItemTypeRemoteSession && it.RemoteSession != nil && it.RemoteSession.ID == id {
			h.cursor = i
			return
		}
	}
	t.Fatalf("no remote session row with id %q; rows=%v", id, visibleRemoteSessionIDs(h, "dev"))
}

func TestShiftUpDownReordersRemoteSession_Issue1875(t *testing.T) {
	h := armHomeForRemoteReorder(t)

	if got := visibleRemoteSessionIDs(h, "dev"); strings.Join(got, ",") != "s-alpha,s-beta,s-gamma" {
		t.Fatalf("precondition: fetched order = %v, want [s-alpha s-beta s-gamma]", got)
	}

	// shift+up on beta must swap it above alpha.
	putCursorOnRemoteSession(t, h, "s-beta")
	h.handleMainKey(tea.KeyMsg{Type: tea.KeyShiftUp})

	if got := visibleRemoteSessionIDs(h, "dev"); strings.Join(got, ",") != "s-beta,s-alpha,s-gamma" {
		t.Fatalf("shift+up on a remote session did not reorder: order = %v, want [s-beta s-alpha s-gamma]", got)
	}

	// The cursor must follow the row the user moved, or a second press moves
	// whatever slid into its place instead.
	if it := h.flatItems[h.cursor]; it.Type != session.ItemTypeRemoteSession || it.RemoteSession == nil || it.RemoteSession.ID != "s-beta" {
		t.Fatalf("cursor did not follow the moved row: cursor is on %+v", it)
	}

	// shift+down puts it back.
	h.handleMainKey(tea.KeyMsg{Type: tea.KeyShiftDown})
	if got := visibleRemoteSessionIDs(h, "dev"); strings.Join(got, ",") != "s-alpha,s-beta,s-gamma" {
		t.Fatalf("shift+down on a remote session did not reorder: order = %v, want [s-alpha s-beta s-gamma]", got)
	}

	// The "J" alias must reach the same path as shift+down.
	putCursorOnRemoteSession(t, h, "s-alpha")
	h.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("J")})
	if got := visibleRemoteSessionIDs(h, "dev"); strings.Join(got, ",") != "s-beta,s-alpha,s-gamma" {
		t.Fatalf("J alias did not reorder a remote session: order = %v, want [s-beta s-alpha s-gamma]", got)
	}
}

// A move that cannot be performed must say so. A silent no-op is the bug this
// issue is about, so every unsupported remote case has to leave a message in
// the footer.
func TestRemoteReorderIsNeverSilent_Issue1875(t *testing.T) {
	cases := []struct {
		name string
		arm  func(t *testing.T, h *Home)
		key  tea.KeyMsg
		want string
	}{
		{
			name: "remote group header",
			arm: func(t *testing.T, h *Home) {
				for i, it := range h.flatItems {
					if it.Type == session.ItemTypeRemoteGroup && it.Path == "remotes/dev/work" {
						h.cursor = i
						return
					}
				}
				t.Fatalf("no remote sub-group header row")
			},
			key:  tea.KeyMsg{Type: tea.KeyShiftUp},
			want: "group",
		},
		{
			name: "already first in group",
			arm: func(t *testing.T, h *Home) {
				putCursorOnRemoteSession(t, h, "s-alpha")
			},
			key:  tea.KeyMsg{Type: tea.KeyShiftUp},
			want: "first",
		},
		{
			name: "already last in group",
			arm: func(t *testing.T, h *Home) {
				putCursorOnRemoteSession(t, h, "s-gamma")
			},
			key:  tea.KeyMsg{Type: tea.KeyShiftDown},
			want: "last",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := armHomeForRemoteReorder(t)
			h.clearError()
			tc.arm(t, h)
			h.handleMainKey(tc.key)

			if h.err == nil {
				t.Fatalf("silent no-op: %s produced no footer message", tc.name)
			}
			if !strings.Contains(strings.ToLower(h.err.Error()), tc.want) {
				t.Fatalf("message %q does not mention %q", h.err.Error(), tc.want)
			}
		})
	}
}
