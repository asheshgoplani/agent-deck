package ui

import (
	"errors"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Regression tests for "f (quick fork) does nothing on remote sessions".
//
// The f-key dispatch in handleMainKey only forked ItemTypeSession rows, so
// pressing f on a remote-session row was a silent no-op. These tests pin the
// remote fork path at the same dispatch boundary used by the remote move and
// remote group-create tests: the key must return the SSH-routed cmd, guard
// against a duplicate fork while one is in flight, and refresh the fleet
// list once the remote confirms. The SSH arguments themselves are covered by
// TestSSHRunnerForkSession_* in the session package.

func armHomeWithOneRemoteSessionForFork(t *testing.T) *Home {
	t.Helper()

	withTempAgentDeckHome(t, `
[remotes.lab]
host = "alice@lab.example"
agent_deck_path = "/usr/local/bin/agent-deck"
`)

	home := NewHome()
	home.width = 120
	home.height = 40
	home.initialLoading = false
	home.remoteSessions = map[string][]session.RemoteSessionInfo{
		"lab": {{ID: "remote-id-xyz", Title: "remote session", Group: "work", Tool: "claude"}},
	}
	home.flatItems = []session.Item{
		{
			Type:       session.ItemTypeRemoteSession,
			RemoteName: "lab",
			RemoteSession: &session.RemoteSessionInfo{
				ID:    "remote-id-xyz",
				Title: "remote session",
				Group: "work",
				Tool:  "claude",
			},
		},
	}
	home.cursor = 0
	return home
}

// TestRemoteFork_FKeyReturnsForkCmd pins the dispatch: f on a remote-session
// row must return the SSH-routed fork cmd, mark the row as forking, and must
// not touch the local instance list.
func TestRemoteFork_FKeyReturnsForkCmd(t *testing.T) {
	home := armHomeWithOneRemoteSessionForFork(t)

	model, cmd := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	if cmd == nil {
		t.Fatal("f on a remote session returned a nil cmd; expected the SSH-routed fork")
	}
	if _, ok := model.(*Home); !ok {
		t.Fatalf("unexpected model type %T", model)
	}
	forkID := remoteRestartAnimationID("lab", "remote-id-xyz")
	if _, forking := home.remoteForking[forkID]; !forking {
		t.Fatal("remote row was not marked as forking (in-flight guard)")
	}
	if _, animated := home.forkingSessions[forkID]; !animated {
		t.Fatal("remote row was not marked as forking (animation)")
	}
	if len(home.instances) != 0 {
		t.Fatalf("local instances mutated by remote fork: %d rows", len(home.instances))
	}
	if home.forkDialog.IsVisible() {
		t.Fatal("quick fork on a remote session must not open the fork dialog")
	}
}

// TestRemoteFork_SecondFIsRefusedWhileInFlight pins the duplicate guard: a
// second f while the remote has not answered yet must not issue another fork.
func TestRemoteFork_SecondFIsRefusedWhileInFlight(t *testing.T) {
	home := armHomeWithOneRemoteSessionForFork(t)

	if _, cmd := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}); cmd == nil {
		t.Fatal("first f returned a nil cmd")
	}
	if _, cmd := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}); cmd != nil {
		t.Fatal("second f while forking returned a cmd; expected the in-flight guard to refuse it")
	}
	if home.err == nil {
		t.Fatal("second f while forking did not explain why it was refused")
	}
}

// TestRemoteFork_ForkedMsgRefreshesFleet pins the confirmation path: once the
// remote reports the fork, the in-flight guard clears and the remote session
// list is fetched again so the new row appears.
func TestRemoteFork_ForkedMsgRefreshesFleet(t *testing.T) {
	home := armHomeWithOneRemoteSessionForFork(t)
	forkID := remoteRestartAnimationID("lab", "remote-id-xyz")
	if _, cmd := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}); cmd == nil {
		t.Fatal("f returned a nil cmd")
	}

	model, cmd := home.Update(remoteSessionForkedMsg{remoteName: "lab", sessionID: "remote-id-xyz", title: "remote session", newID: "child-1"})
	h, ok := model.(*Home)
	if !ok {
		t.Fatalf("unexpected model type %T", model)
	}
	if cmd == nil {
		t.Fatal("successful remote fork returned a nil cmd; expected a remote session refresh")
	}
	if _, forking := h.remoteForking[forkID]; forking {
		t.Fatal("in-flight guard not cleared after the remote confirmed the fork")
	}
	if _, animated := h.forkingSessions[forkID]; animated {
		t.Fatal("fork animation not cleared after the remote confirmed the fork")
	}

	// A second f must be accepted again now that the first fork finished.
	if _, cmd := h.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}); cmd == nil {
		t.Fatal("f after a finished fork returned a nil cmd")
	}
}

// TestRemoteFork_ForkedMsgErrorClearsGuard pins the failure path: the error
// is shown, the guard clears, and no refresh is issued for a fork that never
// happened.
func TestRemoteFork_ForkedMsgErrorClearsGuard(t *testing.T) {
	home := armHomeWithOneRemoteSessionForFork(t)
	forkID := remoteRestartAnimationID("lab", "remote-id-xyz")
	if _, cmd := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}); cmd == nil {
		t.Fatal("f returned a nil cmd")
	}

	model, cmd := home.Update(remoteSessionForkedMsg{remoteName: "lab", sessionID: "remote-id-xyz", title: "remote session", err: errors.New("not a forkable session (tool: gemini)")})
	h := model.(*Home)
	if cmd != nil {
		t.Fatal("failed remote fork returned a cmd; expected no refresh")
	}
	if _, forking := h.remoteForking[forkID]; forking {
		t.Fatal("in-flight guard not cleared after the remote refused the fork")
	}
	if _, animated := h.forkingSessions[forkID]; animated {
		t.Fatal("fork animation not cleared after the remote refused the fork")
	}
	if h.err == nil {
		t.Fatal("remote fork failure was not surfaced")
	}
}

// TestRemoteFork_ShiftFExplainsInsteadOfDialog guards the other fork key: F
// (fork dialog) on a remote row must not open the local-git-backed dialog and
// must point at f instead.
func TestRemoteFork_ShiftFExplainsInsteadOfDialog(t *testing.T) {
	home := armHomeWithOneRemoteSessionForFork(t)

	_, cmd := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'F'}})
	if cmd != nil {
		t.Fatal("F on a remote session returned a cmd")
	}
	if home.forkDialog.IsVisible() {
		t.Fatal("F on a remote session opened the local fork dialog")
	}
	if home.err == nil {
		t.Fatal("F on a remote session gave no hint")
	}
}

// TestRemoteFork_GuardOutlivesAnimationCleanup pins the P1 from review: the
// in-flight guard must not live in forkingSessions alone, because
// cleanupExpiredAnimations drops every key absent from instanceByID on the
// next tick and remote keys are never there. A slow SSH fork that outlives
// the animation must still refuse a second f.
func TestRemoteFork_GuardOutlivesAnimationCleanup(t *testing.T) {
	home := armHomeWithOneRemoteSessionForFork(t)
	forkID := remoteRestartAnimationID("lab", "remote-id-xyz")
	if _, cmd := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}); cmd == nil {
		t.Fatal("first f returned a nil cmd")
	}

	// Model the 2-second tick running while SSH is still in flight.
	home.forkingSessions[forkID] = time.Now().Add(-time.Minute)
	home.cleanupExpiredAnimations(home.forkingSessions, 20*time.Second, 5*time.Second)
	if _, animated := home.forkingSessions[forkID]; animated {
		t.Fatal("expired remote fork animation was not cleaned up")
	}
	if _, inFlight := home.remoteForking[forkID]; !inFlight {
		t.Fatal("animation cleanup released the in-flight remote fork guard")
	}

	if _, cmd := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}); cmd != nil {
		t.Fatal("second f after animation cleanup started a duplicate remote fork")
	}
	if home.err == nil {
		t.Fatal("second f after animation cleanup did not explain why it was refused")
	}

	// The remote's answer (success or error) is the only thing that clears it.
	home.Update(remoteSessionForkedMsg{remoteName: "lab", sessionID: "remote-id-xyz", title: "remote session", newID: "child-1"})
	if _, inFlight := home.remoteForking[forkID]; inFlight {
		t.Fatal("remoteSessionForkedMsg did not clear the in-flight guard")
	}
}
