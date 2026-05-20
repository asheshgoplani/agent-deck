package ui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/terminal"
)

// Issue #1100 (by @ddorman-dn, follow-up to #1098):
//
//	(a) Shift+Enter on a REMOTE session did nothing — the dispatch arm
//	    only handled ItemTypeSession, so the launcher was never
//	    invoked for remote items.
//	(b) Local Shift+Enter opened a new iTerm WINDOW; users expect a
//	    TAB by default.
//
// These tests pin both fixes at the dispatch boundary in home.go.

// withTempAgentDeckHome points $HOME at a fresh tempdir, optionally
// writing a config.toml there, and clears the LoadUserConfig cache so
// the next call reads from the new path. The returned cleanup restores
// both HOME and the cache. Tests must run in t.Cleanup order, so we
// register the cleanup before returning.
func withTempAgentDeckHome(t *testing.T, configTOML string) {
	t.Helper()
	tmpDir := t.TempDir()
	if configTOML != "" {
		dir := filepath.Join(tmpDir, ".agent-deck")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(configTOML), 0o644); err != nil {
			t.Fatalf("write config.toml: %v", err)
		}
	}
	oldHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", tmpDir); err != nil {
		t.Fatalf("setenv HOME: %v", err)
	}
	session.ClearUserConfigCache()
	t.Cleanup(func() {
		_ = os.Setenv("HOME", oldHome)
		session.ClearUserConfigCache()
	})
}

// armHomeWithOneRemoteSession sets up a Home whose cursor sits on a
// remote session item, mirroring armHomeWithOneSession's contract but
// for the remote dispatch path. The remote is named in $HOME's
// config.toml so buildRemoteAttachRequest can resolve it.
func armHomeWithOneRemoteSession(t *testing.T) *Home {
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

	// Synthesize a single remote-session flat item at the cursor.
	home.flatItems = []session.Item{
		{
			Type:       session.ItemTypeRemoteSession,
			RemoteName: "lab",
			RemoteSession: &session.RemoteSessionInfo{
				ID:    "remote-id-xyz",
				Title: "remote session",
			},
		},
	}
	home.cursor = 0
	return home
}

// TestIssue1100_HomeDispatch_ShiftEnterRemoteCallsLauncher pins fix (a):
// pressing Shift+Enter on a remote session must invoke the launcher
// with a Remote-populated AttachRequest that carries the resolved SSH
// host, binary path, profile, and remote session id.
func TestIssue1100_HomeDispatch_ShiftEnterRemoteCallsLauncher(t *testing.T) {
	home := armHomeWithOneRemoteSession(t)

	var called bool
	var captured terminal.AttachRequest
	home.openInNewWindowSink = func(req terminal.AttachRequest) error {
		called = true
		captured = req
		return nil
	}

	keyMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{shiftEnterMarker}}
	_, _ = home.handleMainKey(keyMsg)

	if !called {
		t.Fatal("Shift+Enter on remote session did NOT call the new-window launcher (the #1100a regression)")
	}
	if captured.Remote == nil {
		t.Fatalf("AttachRequest.Remote should be populated for remote dispatch, got %+v", captured)
	}
	if got, want := captured.Remote.Host, "alice@lab.example"; got != want {
		t.Errorf("AttachRequest.Remote.Host = %q, want %q", got, want)
	}
	if got, want := captured.Remote.AgentDeckPath, "/usr/local/bin/agent-deck"; got != want {
		t.Errorf("AttachRequest.Remote.AgentDeckPath = %q, want %q", got, want)
	}
	if got, want := captured.Remote.Profile, "work"; got != want {
		t.Errorf("AttachRequest.Remote.Profile = %q, want %q", got, want)
	}
	if got, want := captured.Name, "remote-id-xyz"; got != want {
		t.Errorf("AttachRequest.Name = %q, want %q (remote session id)", got, want)
	}
}

// TestIssue1100_HomeDispatch_ShiftEnterDefaultsToTab pins fix (b): the
// dispatch path must read [ui] iterm_open_as from user config and pass
// it through to the launcher, with "tab" as the default when unset.
func TestIssue1100_HomeDispatch_ShiftEnterDefaultsToTab(t *testing.T) {
	withTempAgentDeckHome(t, "") // no config => default
	home, _, _ := armHomeWithOneSession(t)

	var captured terminal.AttachRequest
	home.openInNewWindowSink = func(req terminal.AttachRequest) error {
		captured = req
		return nil
	}

	_, _ = home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{shiftEnterMarker}})

	if captured.OpenAs != "tab" {
		t.Fatalf("default OpenAs = %q, want %q (the #1100b regression — window was the old default)", captured.OpenAs, "tab")
	}
}

// TestIssue1100_HomeDispatch_ShiftEnterRespectsWindowConfig pins the
// opt-in path: a user who set iterm_open_as = "window" gets the
// pre-#1100 detached-window behavior.
func TestIssue1100_HomeDispatch_ShiftEnterRespectsWindowConfig(t *testing.T) {
	withTempAgentDeckHome(t, `
[ui]
iterm_open_as = "window"
`)
	home, _, _ := armHomeWithOneSession(t)

	var captured terminal.AttachRequest
	home.openInNewWindowSink = func(req terminal.AttachRequest) error {
		captured = req
		return nil
	}

	_, _ = home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{shiftEnterMarker}})

	if captured.OpenAs != "window" {
		t.Fatalf("OpenAs with iterm_open_as=window = %q, want %q", captured.OpenAs, "window")
	}
}

// TestIssue1100_BuildRemoteAttachRequest_UnknownRemoteReturnsFalse pins
// the safety guard: if the user cursor lands on a remote item whose
// remote name isn't in user config (stale state, deleted remote), we
// must NOT spawn an empty SSH command — the helper returns ok=false
// and the dispatch arm skips the launcher.
func TestIssue1100_BuildRemoteAttachRequest_UnknownRemoteReturnsFalse(t *testing.T) {
	withTempAgentDeckHome(t, "") // no remotes configured

	req, ok := buildRemoteAttachRequest("missing-remote", "some-id", "tab")
	if ok {
		t.Fatalf("expected ok=false for unknown remote, got req=%+v", req)
	}
}

// TestIssue1100_BuildRemoteAttachRequest_EmptyInputsReturnFalse pins
// the input-validation guard.
func TestIssue1100_BuildRemoteAttachRequest_EmptyInputsReturnFalse(t *testing.T) {
	withTempAgentDeckHome(t, `
[remotes.lab]
host = "alice@lab.example"
`)
	if _, ok := buildRemoteAttachRequest("", "id", "tab"); ok {
		t.Errorf("empty remoteName must return false")
	}
	if _, ok := buildRemoteAttachRequest("lab", "", "tab"); ok {
		t.Errorf("empty sessionID must return false")
	}
}
