// Issue #1112 — bug 3 (by @ddorman-dn on v1.9.24): arrow keys + Enter
// for menu navigation in insert mode work for local sessions but appear
// broken on REMOTE. Wire-level tests live in
// internal/session/issue1112_insert_remote_named_keys_test.go; these
// UI-level tests bind the TUI keypress → insertKeySender dispatch path
// for a remote target so a regression in the insert_mode.go switch
// (e.g., a stray return or a missing case) fails loudly here.

package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// remoteCapturingSender records every dispatch for assertion.
type remoteCapturingSender struct {
	texts  []string
	named  []string
	enters int
	closed bool
}

func (r *remoteCapturingSender) SendKeys(text string) error {
	r.texts = append(r.texts, text)
	return nil
}
func (r *remoteCapturingSender) SendNamedKey(key string) error {
	r.named = append(r.named, key)
	return nil
}
func (r *remoteCapturingSender) SendEnter() error {
	r.enters++
	return nil
}
func (r *remoteCapturingSender) Close() error {
	r.closed = true
	return nil
}

// armRemoteInsertMode wires Home with a single REMOTE session as the
// cursor target and stubs the keysender opener to return a capturing
// fake. After this returns, Home is in insert mode against the remote.
func armRemoteInsertMode(t *testing.T) (*Home, *remoteCapturingSender) {
	t.Helper()
	home := NewHome()
	home.width = 100
	home.height = 30

	const remoteName = "dev"
	rs := session.RemoteSessionInfo{
		ID: "r1", Title: "claude-remote", Tool: "claude",
		Status: "waiting", RemoteName: remoteName,
	}
	home.remoteSessionsMu.Lock()
	home.remoteSessions = map[string][]session.RemoteSessionInfo{
		remoteName: {rs},
	}
	home.remoteSessionsMu.Unlock()
	home.rebuildFlatItems()

	// Move cursor to the remote session row (after the group header).
	for i, item := range home.flatItems {
		if item.Type == session.ItemTypeRemoteSession && item.RemoteSession != nil &&
			item.RemoteSession.ID == "r1" {
			home.cursor = i
			break
		}
	}

	fake := &remoteCapturingSender{}
	home.insertOpenKeySender = func(target insertTargetRef) (insertKeySender, error) {
		if !target.isRemote() {
			t.Fatalf("test setup: opener called with non-remote target (%+v)", target)
		}
		return fake, nil
	}
	home.insertKeySink = nil
	home.insertNamedKeySink = nil

	// Press `I` to enter insert mode.
	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'I'}})
	home = model.(*Home)
	if !home.insertMode {
		t.Fatal("test setup: failed to enter insert mode on remote target")
	}
	if home.insertModeRemoteName != remoteName {
		t.Fatalf("test setup: remote name = %q, want %q", home.insertModeRemoteName, remoteName)
	}
	return home, fake
}

// TestIssue1112_RemoteInsertMode_ArrowKeysDispatch verifies the four
// arrow keys reach SendNamedKey on a remote target — the bug 3 happy
// path. If a future refactor drops the remote branch from
// dispatchInsertNamedKey, this test catches it.
func TestIssue1112_RemoteInsertMode_ArrowKeysDispatch(t *testing.T) {
	cases := []struct {
		key  tea.KeyType
		want string
	}{
		{tea.KeyUp, "Up"},
		{tea.KeyDown, "Down"},
		{tea.KeyLeft, "Left"},
		{tea.KeyRight, "Right"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			home, fake := armRemoteInsertMode(t)
			model, _ := home.Update(tea.KeyMsg{Type: tc.key})
			_ = model.(*Home)
			if len(fake.named) != 1 || fake.named[0] != tc.want {
				t.Errorf("dispatched named keys = %v, want [%q] (#1112 bug 3)", fake.named, tc.want)
			}
		})
	}
}

// TestIssue1112_RemoteInsertMode_EnterDispatches verifies pressing Enter
// while in remote insert mode goes through SendEnter (not SendKeys
// "\n", not SendNamedKey "Enter") — the remote-side handler counts on
// the discrete --enter verb for the bracketed-paste flush.
func TestIssue1112_RemoteInsertMode_EnterDispatches(t *testing.T) {
	home, fake := armRemoteInsertMode(t)
	model, _ := home.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_ = model.(*Home)
	if fake.enters != 1 {
		t.Errorf("SendEnter calls = %d, want 1 (#1112 bug 3)", fake.enters)
	}
	if len(fake.named) != 0 {
		t.Errorf("Enter must not dispatch as a named key; got named=%v", fake.named)
	}
}

// TestIssue1112_RemoteInsertMode_TabDispatches verifies Tab goes through
// the named-key path with the tmux name "Tab" (not the literal "\t").
func TestIssue1112_RemoteInsertMode_TabDispatches(t *testing.T) {
	home, fake := armRemoteInsertMode(t)
	model, _ := home.Update(tea.KeyMsg{Type: tea.KeyTab})
	_ = model.(*Home)
	if len(fake.named) != 1 || fake.named[0] != "Tab" {
		t.Errorf("Tab dispatched as %v, want [Tab]", fake.named)
	}
}

// TestIssue1112_RemoteInsertMode_FullMenuNavSequence simulates a real
// user picking the third item in a picker: Down, Down, Enter. All three
// dispatches must land in order on the remote sender.
func TestIssue1112_RemoteInsertMode_FullMenuNavSequence(t *testing.T) {
	home, fake := armRemoteInsertMode(t)
	for _, key := range []tea.KeyType{tea.KeyDown, tea.KeyDown, tea.KeyEnter} {
		model, _ := home.Update(tea.KeyMsg{Type: key})
		home = model.(*Home)
	}
	if got := fake.named; len(got) != 2 || got[0] != "Down" || got[1] != "Down" {
		t.Errorf("named = %v, want [Down Down]", got)
	}
	if fake.enters != 1 {
		t.Errorf("enters = %d, want 1", fake.enters)
	}
}
