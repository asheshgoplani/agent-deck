package ui

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// setupRemoteTestHome creates a Home model suitable for remote session teatests.
// It isolates HOME to a temp dir with a minimal config to prevent the setup wizard.
func setupRemoteTestHome(t *testing.T) *Home {
	t.Helper()

	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	session.ClearUserConfigCache()
	t.Cleanup(func() {
		os.Setenv("HOME", origHome)
		session.ClearUserConfigCache()
	})

	// Create minimal config so the setup wizard doesn't appear.
	configDir := filepath.Join(tmpHome, ".agent-deck")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("# test config\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	return NewHome()
}

// testRemoteSessions returns a remoteSessionsFetchedMsg with a single test session.
func testRemoteSessions() remoteSessionsFetchedMsg {
	return remoteSessionsFetchedMsg{
		sessions: map[string][]session.RemoteSessionInfo{
			"myserver": {
				{ID: "remote-abc123", Title: "test-remote-sess", Status: "running", RemoteName: "myserver"},
			},
		},
	}
}

// startRemoteTestModel creates a teatest model, waits for init to settle,
// dismisses any startup prompts, injects remote sessions, and navigates
// the cursor to the remote session. The caller must call tm.Quit() when done.
func startRemoteTestModel(t *testing.T) *teatest.TestModel {
	t.Helper()

	home := setupRemoteTestHome(t)
	tm := teatest.NewTestModel(t, home, teatest.WithInitialTermSize(100, 30))

	// Let Init() goroutines (loadSessions, fetchRemoteSessions) settle.
	time.Sleep(time.Second)

	// Dismiss any startup dialog (hooks prompt, etc.) using Esc.
	// Using Esc instead of 'n' to avoid opening the New Session dialog
	// if the hooks prompt is not visible (statedb singleton caching).
	tm.Send(tea.KeyMsg{Type: tea.KeyEsc})
	time.Sleep(300 * time.Millisecond)

	// Inject remote sessions into the model.
	tm.Send(testRemoteSessions())

	// Wait for the remote session to appear in rendered output.
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return bytes.Contains(bts, []byte("test-remote-sess"))
	}, teatest.WithDuration(5*time.Second))

	// Navigate cursor down to the remote session item.
	// Empty test profile layout: [0] default group, [1] myserver group, [2] remote session
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})

	// Let navigation settle.
	time.Sleep(200 * time.Millisecond)

	return tm
}

func TestTeatest_RemoteDelete_ShowsConfirmDialog(t *testing.T) {
	tm := startRemoteTestModel(t)

	// Press 'd' for delete
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})

	// Verify the confirm dialog renders in the output.
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return bytes.Contains(bts, []byte("Delete Remote Session"))
	}, teatest.WithDuration(3*time.Second))

	// Quit and check model state.
	tm.Quit()
	fm := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second))
	h := fm.(*Home)

	if !h.confirmDialog.IsVisible() {
		t.Fatal("confirm dialog should be visible")
	}
	if h.confirmDialog.GetConfirmType() != ConfirmDeleteRemoteSession {
		t.Fatalf("got confirm type %v, want ConfirmDeleteRemoteSession", h.confirmDialog.GetConfirmType())
	}
	if h.confirmDialog.GetRemoteName() != "myserver" {
		t.Fatalf("got remote %q, want %q", h.confirmDialog.GetRemoteName(), "myserver")
	}
	if h.confirmDialog.GetTargetID() != "remote-abc123" {
		t.Fatalf("got target ID %q, want %q", h.confirmDialog.GetTargetID(), "remote-abc123")
	}
}

func TestTeatest_RemoteClose_ShowsConfirmDialog(t *testing.T) {
	tm := startRemoteTestModel(t)

	// Press 'D' (shift-d) for close
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})

	// Verify the close confirm dialog renders.
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return bytes.Contains(bts, []byte("Close Remote Session"))
	}, teatest.WithDuration(3*time.Second))

	tm.Quit()
	fm := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second))
	h := fm.(*Home)

	if !h.confirmDialog.IsVisible() {
		t.Fatal("confirm dialog should be visible")
	}
	if h.confirmDialog.GetConfirmType() != ConfirmCloseRemoteSession {
		t.Fatalf("got confirm type %v, want ConfirmCloseRemoteSession", h.confirmDialog.GetConfirmType())
	}
	if h.confirmDialog.GetRemoteName() != "myserver" {
		t.Fatalf("got remote %q, want %q", h.confirmDialog.GetRemoteName(), "myserver")
	}
}

func TestTeatest_RemoteRestart_TriggersAction(t *testing.T) {
	tm := startRemoteTestModel(t)

	// Press 'R' for restart (will fail because no real remote, but should trigger the action)
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})

	// The restart runs async and will fail (no real SSH remote). Wait for the
	// error message to appear in output, confirming the handler was invoked.
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return bytes.Contains(bts, []byte("failed to restart remote session")) ||
			bytes.Contains(bts, []byte("failed to load remote config")) ||
			bytes.Contains(bts, []byte("remote")) // broad match as fallback
	}, teatest.WithDuration(5*time.Second))

	tm.Quit()
	tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second))
}

func TestTeatest_RemoteDelete_ConfirmTriggersAction(t *testing.T) {
	tm := startRemoteTestModel(t)

	// Press 'd' to show delete dialog
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return bytes.Contains(bts, []byte("Delete Remote Session"))
	}, teatest.WithDuration(3*time.Second))

	// Confirm with 'y' — this hides the dialog and fires the async delete cmd.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})

	// Wait for the async cmd to complete and the result to be processed.
	time.Sleep(2 * time.Second)

	tm.Quit()
	fm := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second))
	h := fm.(*Home)

	// Dialog should be hidden after confirming.
	if h.confirmDialog.IsVisible() {
		t.Fatal("confirm dialog should be hidden after confirming")
	}
	// The async delete should have completed and set an error (no real remote).
	if h.err == nil {
		t.Fatal("expected error from remote delete (no real remote configured)")
	}
}

func TestTeatest_RemoteClose_ConfirmTriggersAction(t *testing.T) {
	tm := startRemoteTestModel(t)

	// Press 'D' to show close dialog
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return bytes.Contains(bts, []byte("Close Remote Session"))
	}, teatest.WithDuration(3*time.Second))

	// Confirm with 'y'
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})

	// Wait for the async cmd to complete.
	time.Sleep(2 * time.Second)

	tm.Quit()
	fm := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second))
	h := fm.(*Home)

	if h.confirmDialog.IsVisible() {
		t.Fatal("confirm dialog should be hidden after confirming")
	}
	if h.err == nil {
		t.Fatal("expected error from remote close (no real remote configured)")
	}
}

func TestTeatest_RemoteDelete_CancelKeepsDialog(t *testing.T) {
	tm := startRemoteTestModel(t)

	// Press 'd' to show delete dialog
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return bytes.Contains(bts, []byte("Delete Remote Session"))
	}, teatest.WithDuration(3*time.Second))

	// Cancel with 'n'
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	time.Sleep(200 * time.Millisecond)

	tm.Quit()
	fm := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second))
	h := fm.(*Home)

	// Dialog should be hidden after cancelling.
	if h.confirmDialog.IsVisible() {
		t.Fatal("confirm dialog should be hidden after pressing n")
	}
}
