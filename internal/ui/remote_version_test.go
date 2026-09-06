package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// #2164: the remote host header shows the remote's version with an arrow
// when it runs an older release than this controller, and `u` on that
// header offers the update through the standard confirm dialog.

func withControllerVersion(t *testing.T, v string) {
	t.Helper()
	orig := Version
	SetVersion(v)
	t.Cleanup(func() { SetVersion(orig) })
}

// armHomeOnRemoteHeader parks the cursor on the "remotes/lab" host header
// with the remote reporting remoteVersion (empty means never checked).
func armHomeOnRemoteHeader(t *testing.T, remoteVersion string) *Home {
	t.Helper()
	withTempAgentDeckHome(t, `
[remotes.lab]
host = "alice@lab.example"
agent_deck_path = "/usr/local/bin/agent-deck"
`)
	withControllerVersion(t, "1.16.0")

	home := NewHome()
	home.width = 120
	home.height = 40
	home.initialLoading = false
	home.remoteSessions = map[string][]session.RemoteSessionInfo{
		"lab": {
			{ID: "r1", Title: "one", Group: "work", Status: "running"},
			{ID: "r2", Title: "two", Group: "work", Status: "idle"},
			{ID: "r3", Title: "three", Group: "", Status: "idle"},
		},
	}
	if remoteVersion != "" {
		home.remoteVersions = map[string]session.RemoteVersionState{
			"lab": {Version: remoteVersion, Found: true, CheckedAt: time.Now()},
		}
	}
	home.flatItems = []session.Item{
		{Type: session.ItemTypeRemoteGroup, RemoteName: "lab", Path: "remotes/lab", Level: 0},
	}
	home.cursor = 0
	return home
}

func renderRemoteHeader(home *Home) string {
	var b strings.Builder
	home.renderRemoteGroupItem(&b, home.flatItems[0], false)
	return b.String()
}

func TestRemoteHeader_ShowsVersionArrowOnDrift(t *testing.T) {
	home := armHomeOnRemoteHeader(t, "1.15.0")
	got := renderRemoteHeader(home)
	if !strings.Contains(got, "remotes/lab") || !strings.Contains(got, "(3)") {
		t.Fatalf("header lost its name/count: %q", got)
	}
	if !strings.Contains(got, "v1.15.0 ↑") {
		t.Errorf("header must carry the drift marker after the count: %q", got)
	}
	if idx := strings.Index(got, "(3)"); idx < 0 || strings.Index(got, "v1.15.0 ↑") < idx {
		t.Errorf("marker must follow the count: %q", got)
	}
}

func TestRemoteHeader_NoMarkerWhenCurrentOrUnknown(t *testing.T) {
	for name, version := range map[string]string{"current": "1.16.0", "newer": "1.16.1", "unknown": ""} {
		t.Run(name, func(t *testing.T) {
			got := renderRemoteHeader(armHomeOnRemoteHeader(t, version))
			if strings.Contains(got, "↑") || (version != "" && strings.Contains(got, "v"+version)) {
				t.Errorf("no drift marker expected for %s: %q", name, got)
			}
		})
	}
}

func TestRemoteVersionMarker(t *testing.T) {
	cases := []struct {
		state      session.RemoteVersionState
		controller string
		want       string
	}{
		{session.RemoteVersionState{Version: "1.15.0", Found: true}, "1.16.0", " v1.15.0 ↑"},
		{session.RemoteVersionState{Version: "1.16.0", Found: true}, "1.16.0", ""},
		{session.RemoteVersionState{Found: false}, "1.16.0", ""},
		{session.RemoteVersionState{Version: "1.15.0", Found: true}, "0.0.0", ""},
	}
	for _, tc := range cases {
		if got := remoteVersionMarker(tc.state, tc.controller); got != tc.want {
			t.Errorf("remoteVersionMarker(%+v, %q) = %q, want %q", tc.state, tc.controller, got, tc.want)
		}
	}
}

func TestRemoteVersionStale_OncePerHour(t *testing.T) {
	now := time.Now()
	if !remoteVersionStale(session.RemoteVersionState{}, false, now) {
		t.Error("never checked must be stale")
	}
	if remoteVersionStale(session.RemoteVersionState{CheckedAt: now.Add(-10 * time.Minute)}, true, now) {
		t.Error("checked ten minutes ago must not be re-asked on this tick")
	}
	if !remoteVersionStale(session.RemoteVersionState{CheckedAt: now.Add(-remoteVersionCheckInterval)}, true, now) {
		t.Error("checked an hour ago must be re-asked")
	}
}

func TestRemoteHeader_UOpensUpdateDialogOnlyOnDrift(t *testing.T) {
	t.Run("drift", func(t *testing.T) {
		home := armHomeOnRemoteHeader(t, "1.15.0")
		_, _ = home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
		if !home.confirmDialog.IsVisible() || home.confirmDialog.GetConfirmType() != ConfirmUpdateRemote {
			t.Fatal("u on a drifted remote header must open the update confirm dialog")
		}
		if home.confirmDialog.GetRemoteName() != "lab" || home.confirmDialog.targetName != "1.15.0" || home.confirmDialog.GetTargetID() != "1.16.0" {
			t.Errorf("dialog carries wrong remote/versions: %s %s -> %s", home.confirmDialog.GetRemoteName(), home.confirmDialog.targetName, home.confirmDialog.GetTargetID())
		}
		home.confirmDialog.SetSize(120, 40)
		if view := home.confirmDialog.View(); !strings.Contains(view, "Update remote lab from v1.15.0 to v1.16.0?") {
			t.Errorf("dialog text = %q", view)
		}
	})
	for name, version := range map[string]string{"current": "1.16.0", "unknown": ""} {
		t.Run(name, func(t *testing.T) {
			home := armHomeOnRemoteHeader(t, version)
			_, _ = home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
			if home.confirmDialog.IsVisible() {
				t.Fatalf("u must be a no-op on a %s remote header", name)
			}
		})
	}
}

type deployStub struct {
	calls  int
	name   string
	target string
	err    error
}

func (s *deployStub) install(t *testing.T) {
	t.Helper()
	orig := deployRemoteUpdate
	deployRemoteUpdate = func(_ context.Context, name string, _ session.RemoteConfig, target string) (string, error) {
		s.calls++
		s.name = name
		s.target = target
		return target, s.err
	}
	t.Cleanup(func() { deployRemoteUpdate = orig })
}

func TestRemoteHeader_ConfirmRunsDeploy(t *testing.T) {
	home := armHomeOnRemoteHeader(t, "1.15.0")
	stub := &deployStub{}
	stub.install(t)

	_, _ = home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	_, cmd := home.handleConfirmDialogKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if home.confirmDialog.IsVisible() {
		t.Fatal("confirming must close the dialog")
	}
	if cmd == nil {
		t.Fatal("confirming must return the update command")
	}
	if stub.calls != 0 {
		t.Fatal("the deploy runs inside the command, not on the key press")
	}

	msg, ok := cmd().(remoteUpdatedMsg)
	if !ok {
		t.Fatalf("cmd returned %T, want remoteUpdatedMsg", msg)
	}
	if stub.calls != 1 || stub.name != "lab" || stub.target != "1.16.0" {
		t.Fatalf("deploy stub calls=%d name=%q target=%q, want one call for lab -> 1.16.0", stub.calls, stub.name, stub.target)
	}
	if msg.err != nil || msg.remoteName != "lab" || msg.from != "1.15.0" || msg.to != "1.16.0" {
		t.Errorf("result = %+v", msg)
	}

	// Completion records the new version (marker disappears) and refreshes.
	_, refresh := home.Update(msg)
	if refresh == nil {
		t.Error("a successful update must trigger a remote refresh")
	}
	if got := renderRemoteHeader(home); strings.Contains(got, "↑") {
		t.Errorf("marker must clear once the remote runs the controller's version: %q", got)
	}
	if session.LoadRemoteVersions()["lab"].Version != "1.16.0" {
		t.Error("the new version must reach the shared cache")
	}
}

func TestRemoteHeader_ConfirmReportsDeployFailure(t *testing.T) {
	home := armHomeOnRemoteHeader(t, "1.15.0")
	stub := &deployStub{err: errors.New("deploy failed: permission denied")}
	stub.install(t)

	_, _ = home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	_, cmd := home.handleConfirmDialogKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Enter on the focused Cancel button must not run the update")
	}
	if stub.calls != 0 {
		t.Fatal("cancel via Enter must not deploy")
	}

	_, _ = home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	_, cmd = home.handleConfirmDialogKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	msg := cmd().(remoteUpdatedMsg)
	if msg.err == nil || stub.calls != 1 {
		t.Fatalf("failure must surface: %+v (calls=%d)", msg, stub.calls)
	}
	_, refresh := home.Update(msg)
	if refresh != nil {
		t.Error("a failed update must not trigger a refresh")
	}
	if got := renderRemoteHeader(home); !strings.Contains(got, "v1.15.0 ↑") {
		t.Errorf("a failed update keeps the drift marker: %q", got)
	}
}

func TestRemoteHeader_CancelNeverDeploys(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'n'}},
		{Type: tea.KeyEsc},
	} {
		home := armHomeOnRemoteHeader(t, "1.15.0")
		stub := &deployStub{}
		stub.install(t)
		_, _ = home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
		_, cmd := home.handleConfirmDialogKey(key)
		if cmd != nil || home.confirmDialog.IsVisible() {
			t.Fatalf("%s must close the dialog without a command", key)
		}
		if stub.calls != 0 {
			t.Fatalf("%s must not deploy", key)
		}
	}
}

// The remote poll's version answers land in the header map and the shared
// cache; remotes without a fresh answer keep their previous state.
func TestRemoteSessionsFetchedMsg_RecordsVersions(t *testing.T) {
	home := armHomeOnRemoteHeader(t, "1.15.0")
	_, _ = home.Update(remoteSessionsFetchedMsg{
		sessions: map[string][]session.RemoteSessionInfo{"lab": {}},
		versions: map[string]session.RemoteVersionState{"lab": {Version: "1.16.0", Found: true, CheckedAt: time.Now()}},
	})
	if state, ok := home.remoteVersionState("lab"); !ok || state.Version != "1.16.0" {
		t.Fatalf("version not recorded: %+v", state)
	}
	if session.LoadRemoteVersions()["lab"].Version != "1.16.0" {
		t.Error("version must be persisted for remote list")
	}
	_, _ = home.Update(remoteSessionsFetchedMsg{sessions: map[string][]session.RemoteSessionInfo{"lab": {}}})
	if state, _ := home.remoteVersionState("lab"); state.Version != "1.16.0" {
		t.Errorf("a poll without a version answer must keep the previous one: %+v", state)
	}
}
