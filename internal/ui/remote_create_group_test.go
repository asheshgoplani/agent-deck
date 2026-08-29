package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Regression tests for "g (create group) only creates LOCAL groups, even when
// the cursor is on a remote row".
//
// The g-key dispatch in handleMainKey offered the create dialog without any
// remote awareness, so confirming it created the group in the local tree via
// GroupDialogCreate → groupTree.CreateGroup even though the cursor sat on a
// remote session/header whose groups live in the remote's own state DB. These
// tests pin the remote create path at the same dispatch boundary used by the
// remote-move (#2081) and remote-shift-enter (#1100) tests: the dialog must
// carry the remote name, and Enter must return an SSH-routed tea.Cmd instead
// of mutating the local tree.

// TestRemoteCreateGroup_GKeyOnRemoteSessionRoutesOverSSH pins the dispatch:
// g on a remote-session row must open the create dialog scoped to that remote
// (RemoteName == "lab"), and confirming it must return the SSH-routed create
// cmd without touching the local group tree.
func TestRemoteCreateGroup_GKeyOnRemoteSessionRoutesOverSSH(t *testing.T) {
	home := armHomeWithOneRemoteSessionInGroups(t)

	if _, _ = home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}}); !home.groupDialog.IsVisible() {
		t.Fatal("g on a remote session did not open the create dialog")
	}
	if home.groupDialog.Mode() != GroupDialogCreate {
		t.Fatalf("expected GroupDialogCreate mode, got %v", home.groupDialog.Mode())
	}
	if got := home.groupDialog.RemoteName(); got != "lab" {
		t.Fatalf("create dialog remoteName = %q, want \"lab\"", got)
	}
	// The helper's cursor session lives in group "work", so the dialog
	// defaults to root mode with Tab toggling to a subgroup under "work".
	if home.groupDialog.CanToggle() {
		if got := home.groupDialog.GetParentPath(); got != "" {
			t.Fatalf("root-mode create should have no parent, got %q", got)
		}
	} else {
		t.Fatal("grouped remote session create dialog should offer the Root/Subgroup toggle")
	}

	home.groupDialog.nameInput.SetValue("newgroup")
	model, cmd := home.handleGroupDialogKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter on the remote create dialog returned a nil cmd; expected the SSH-routed create")
	}
	if _, ok := model.(*Home); !ok {
		t.Fatalf("unexpected model type %T", model)
	}

	// The local tree must be untouched: the group lives on the remote.
	if len(home.instances) != 0 {
		t.Fatalf("local instances mutated by remote group create: %d rows", len(home.instances))
	}
}

// TestRemoteCreateGroup_GKeyOnRemoteGroupHeaderDefaultsToSubgroup pins the
// header path: g on a remote group header opens the create dialog scoped to
// that remote WITH the header's group as the parent (subgroup mode), so Enter
// runs `group create <name> --parent <header>` on the remote.
func TestRemoteCreateGroup_GKeyOnRemoteGroupHeaderDefaultsToSubgroup(t *testing.T) {
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

	// Cursor sits on the "work" group header of remote "lab" (the session
	// row keeps the header present in the flat list, as in the real TUI).
	home.flatItems = []session.Item{
		{
			Type:       session.ItemTypeRemoteSession,
			RemoteName: "lab",
			Path:       "remotes/lab/work",
			RemoteSession: &session.RemoteSessionInfo{
				ID:    "remote-id-xyz",
				Title: "remote session",
				Group: "work",
			},
		},
		{Type: session.ItemTypeRemoteGroup, RemoteName: "lab", Path: "remotes/lab/work", Level: 1},
	}
	home.cursor = 1

	if _, _ = home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}}); !home.groupDialog.IsVisible() {
		t.Fatal("g on a remote group header did not open the create dialog")
	}
	if got := home.groupDialog.RemoteName(); got != "lab" {
		t.Fatalf("create dialog remoteName = %q, want \"lab\"", got)
	}
	if !home.groupDialog.HasParent() {
		t.Fatal("remote group header create should default to subgroup mode")
	}
	if got := home.groupDialog.GetParentPath(); got != "work" {
		t.Fatalf("create dialog parent path = %q, want \"work\"", got)
	}

	home.groupDialog.nameInput.SetValue("sub")
	_, cmd := home.handleGroupDialogKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter on the remote subgroup create dialog returned a nil cmd; expected the SSH-routed create")
	}
}

// TestRemoteCreateGroup_GKeyOnRemoteHostHeaderCreatesRootGroup pins the
// level-0 host-header path: g on "remotes/<name>" (the host header itself)
// opens a root-level remote create with no parent.
func TestRemoteCreateGroup_GKeyOnRemoteHostHeaderCreatesRootGroup(t *testing.T) {
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

	home.flatItems = []session.Item{
		{Type: session.ItemTypeRemoteGroup, RemoteName: "lab", Path: "remotes/lab", Level: 0},
	}
	home.cursor = 0

	if _, _ = home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}}); !home.groupDialog.IsVisible() {
		t.Fatal("g on the remote host header did not open the create dialog")
	}
	if got := home.groupDialog.RemoteName(); got != "lab" {
		t.Fatalf("create dialog remoteName = %q, want \"lab\"", got)
	}
	if home.groupDialog.HasParent() {
		t.Fatalf("host-header create should be root-level, got parent %q", home.groupDialog.GetParentPath())
	}
}

// TestRemoteCreateGroup_LocalSessionStillCreatesLocally guards the other side
// of the branch: g on a LOCAL session keeps creating the group in the local
// tree, with no remote name and no SSH cmd.
func TestRemoteCreateGroup_LocalSessionStillCreatesLocally(t *testing.T) {
	withTempAgentDeckHome(t, "")

	home := NewHome()
	home.width = 120
	home.height = 40
	home.initialLoading = false

	home.groupTree = session.NewGroupTree([]*session.Instance{})
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

	if _, _ = home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}}); !home.groupDialog.IsVisible() {
		t.Fatal("g on a local session did not open the create dialog")
	}
	if got := home.groupDialog.RemoteName(); got != "" {
		t.Fatalf("local create dialog remoteName = %q, want \"\"", got)
	}

	home.groupDialog.nameInput.SetValue("newgroup")
	_, cmd := home.handleGroupDialogKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("local group create returned a non-nil cmd %v; local creates are synchronous", cmd)
	}
}

// TestRemoteGroupResultMsgPatchesCache pins the create confirmation: when the
// remote reports a successful `group create`, the new path is added to the
// cached group list so the M move dialog offers it immediately (before the
// next fleet poll re-confirms from the remote's own DB).
func TestRemoteGroupResultMsgPatchesCache(t *testing.T) {
	home := NewHome()
	home.width = 120
	home.height = 40
	home.initialLoading = false

	home.remoteSessionsMu.Lock()
	home.remoteGroups = map[string][]string{"lab": {"work"}}
	home.remoteSessionsMu.Unlock()

	model, _ := home.Update(remoteGroupResultMsg{remoteName: "lab", groupPath: "work/newgroup"})
	h, ok := model.(*Home)
	if !ok {
		t.Fatalf("unexpected model type %T", model)
	}

	h.remoteSessionsMu.RLock()
	got := append([]string(nil), h.remoteGroups["lab"]...)
	h.remoteSessionsMu.RUnlock()

	want := []string{"work", "work/newgroup"}
	if len(got) != len(want) {
		t.Fatalf("remoteGroups[lab] = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("remoteGroups[lab][%d] = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}
