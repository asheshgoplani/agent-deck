package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Regression tests for "M (move to group) does nothing on remote sessions".
//
// The M-key dispatch in handleMainKey only offered the move dialog for
// ItemTypeSession, so pressing M on a remote-session row was a silent no-op
// even though the remote-rename path (GroupDialogRenameSession) already knows
// how to mutate a remote session over SSH. These tests pin the remote move
// path at the same dispatch boundary used by the #1100 remote-shift-enter
// tests.

// armHomeWithOneRemoteSessionInGroups sets up a Home whose cursor sits on a
// remote session row, with a populated remote fleet cache so remoteGroupPaths
// has something to offer. The remote is declared in $HOME's config.toml so
// any executed SSH command can resolve it (tests that would execute SSH only
// assert the returned tea.Cmd is non-nil and never run it).
func armHomeWithOneRemoteSessionInGroups(t *testing.T) *Home {
	t.Helper()

	withTempAgentDeckHome(t, `
[remotes.lab]
host = "alice@lab.example"
agent_deck_path = "/usr/local/bin/agent-deck"
profile = "work"
`)

	home := NewHome()
	home.width = 120
	home.height = 40
	home.initialLoading = false

	// Remote fleet cache: two sessions across two groups, plus the row the
	// cursor will sit on.
	home.remoteSessions = map[string][]session.RemoteSessionInfo{
		"lab": {
			{ID: "remote-id-xyz", Title: "remote session", Group: "work"},
			{ID: "remote-id-abc", Title: "other session", Group: "personal"},
			{ID: "remote-id-ungrouped", Title: "loose session", Group: ""},
		},
	}
	home.flatItems = []session.Item{
		{
			Type:       session.ItemTypeRemoteSession,
			RemoteName: "lab",
			RemoteSession: &session.RemoteSessionInfo{
				ID:    "remote-id-xyz",
				Title: "remote session",
				Group: "work",
			},
		},
	}
	home.cursor = 0
	return home
}

// TestRemoteMoveToGroup_MKeyOpensDialogWithRemoteGroups pins the dispatch:
// pressing M on a remote-session row must open the move dialog (the same one
// local sessions get) with the groups observed on that remote.
func TestRemoteMoveToGroup_MKeyOpensDialogWithRemoteGroups(t *testing.T) {
	home := armHomeWithOneRemoteSessionInGroups(t)

	if _, _ = home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'M'}}); !home.groupDialog.IsVisible() {
		t.Fatal("M on a remote session did not open the move dialog")
	}
	if home.groupDialog.Mode() != GroupDialogMove {
		t.Fatalf("expected GroupDialogMove mode, got %v", home.groupDialog.Mode())
	}

	// The dialog must offer the remote's own groups (normalized, sorted,
	// deduped) — not the local group tree, which knows nothing about the
	// remote. Empty remote group normalizes to the default group path.
	want := []string{"my-sessions", "personal", "work"}
	got := home.groupDialog.groupPaths
	if len(got) != len(want) {
		t.Fatalf("dialog offered %d group paths %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dialog groupPaths[%d] = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

// TestRemoteMoveToGroup_MKeyIncludesEmptyGroups pins the empty-folder fix:
// the move dialog must offer groups that currently hold NO sessions on the
// remote (a folder left empty after every session was moved out of it).
// Session-derived buckets can never contain an empty group, so this relies on
// the fleet poll's cached `group list --json` (Home.remoteGroups) being
// unioned into remoteGroupPaths.
func TestRemoteMoveToGroup_MKeyIncludesEmptyGroups(t *testing.T) {
	home := armHomeWithOneRemoteSessionInGroups(t)

	// The remote's own group DB reports an extra EMPTY folder (no sessions):
	// it must still be offered as a move target.
	home.remoteSessionsMu.Lock()
	home.remoteGroups = map[string][]string{
		"lab": {"empty-folder", "personal", "work"},
	}
	home.remoteSessionsMu.Unlock()

	if _, _ = home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'M'}}); !home.groupDialog.IsVisible() {
		t.Fatal("M on a remote session did not open the move dialog")
	}

	// Union of the remote's own group list (incl. the empty folder) and the
	// session-derived groups (empty remote group normalizes to my-sessions).
	want := []string{"empty-folder", "my-sessions", "personal", "work"}
	got := home.groupDialog.groupPaths
	if len(got) != len(want) {
		t.Fatalf("dialog offered %d group paths %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dialog groupPaths[%d] = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

// TestRemoteSessionsFetchedMsgPopulatesGroupCache pins the fleet-poll wiring:
// the remote group lists carried on remoteSessionsFetchedMsg land in
// Home.remoteGroups (guarded by remoteSessionsMu), which is what the M move
// dialog reads to offer empty remote folders.
func TestRemoteSessionsFetchedMsgPopulatesGroupCache(t *testing.T) {
	home := NewHome()
	home.width = 120
	home.height = 40
	home.initialLoading = false

	model, _ := home.Update(remoteSessionsFetchedMsg{
		sessions: map[string][]session.RemoteSessionInfo{
			"lab": {{ID: "x", Title: "s", Group: "work"}},
		},
		groups: map[string][]string{
			"lab": {"empty-folder", "work"},
		},
	})
	h, ok := model.(*Home)
	if !ok {
		t.Fatalf("unexpected model type %T", model)
	}

	h.remoteSessionsMu.RLock()
	got := append([]string(nil), h.remoteGroups["lab"]...)
	h.remoteSessionsMu.RUnlock()

	want := []string{"empty-folder", "work"}
	if len(got) != len(want) {
		t.Fatalf("remoteGroups[lab] = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("remoteGroups[lab][%d] = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

// TestRemoteMoveToGroup_EnterReturnsMoveCmd pins the submit path: confirming
// the dialog on a remote row must return a tea.Cmd (the SSH-routed move) and
// must NOT mutate the local group tree.
func TestRemoteMoveToGroup_EnterReturnsMoveCmd(t *testing.T) {
	home := armHomeWithOneRemoteSessionInGroups(t)

	if _, _ = home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'M'}}); !home.groupDialog.IsVisible() {
		t.Fatal("M on a remote session did not open the move dialog")
	}

	model, cmd := home.handleGroupDialogKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter on the remote move dialog returned a nil cmd; expected the SSH-routed move")
	}
	if _, ok := model.(*Home); !ok {
		t.Fatalf("unexpected model type %T", model)
	}

	// The local tree must be untouched: the remote row is not a local
	// instance, so MoveSessionToGroup must never have been called.
	if len(home.instances) != 0 {
		t.Fatalf("local instances mutated by remote move: %d rows", len(home.instances))
	}
}

// TestRemoteMoveToGroup_LocalSessionStillUsesLocalTree guards the other side
// of the branch: moving a LOCAL session must keep using the local tree and
// return no SSH cmd.
func TestRemoteMoveToGroup_LocalSessionStillUsesLocalTree(t *testing.T) {
	withTempAgentDeckHome(t, "")

	home := NewHome()
	home.width = 120
	home.height = 40
	home.initialLoading = false

	inst := &session.Instance{
		ID:          "local-id-1",
		Title:       "local session",
		ProjectPath: "/tmp/local-proj",
		GroupPath:   "work",
	}
	home.instances = []*session.Instance{inst}
	home.flatItems = []session.Item{
		{Type: session.ItemTypeSession, Session: inst},
	}
	home.cursor = 0

	if _, _ = home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'M'}}); !home.groupDialog.IsVisible() {
		t.Fatal("M on a local session did not open the move dialog")
	}

	_, cmd := home.handleGroupDialogKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("local move returned a non-nil cmd %v; local moves are synchronous", cmd)
	}
}
