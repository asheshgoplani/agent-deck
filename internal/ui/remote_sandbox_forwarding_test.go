package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The new-session dialog opened on a remote target (#1353) shows the
// "Run in Docker sandbox" checkbox, but the remote-create path used to read
// only tool/title/path/group and dropped the checkbox: the session started on
// the remote host unsandboxed. These tests drive the real dialog and capture
// what the submit handler forwards to the remote-create path.

type remoteCreateCapture struct {
	calls                                int
	remoteName, tool, title, path, group string
	sandbox                              bool
}

func (c *remoteCreateCapture) sink(remoteName, tool, title, path, group string, sandbox bool) tea.Cmd {
	c.calls++
	c.remoteName, c.tool, c.title, c.path, c.group, c.sandbox = remoteName, tool, title, path, group, sandbox
	return nil
}

// openRemoteDialogAndTypeName opens the dialog via `n` on a remote group and
// types a session name, leaving focus on the Name field.
func openRemoteDialogAndTypeName(t *testing.T, remoteName, name string) (*Home, *remoteCreateCapture) {
	t.Helper()
	setXDGTestHome(t)
	home := NewHome()
	home.width = 100
	home.height = 30
	home.flatItems = []session.Item{remoteGroupItem(remoteName)}
	home.cursor = 0
	capture := &remoteCreateCapture{}
	home.remoteCreateSink = capture.sink

	h := pressN(t, home)
	if !h.newDialog.IsVisible() {
		t.Fatal("precondition: n on a remote group must open the dialog")
	}
	for _, r := range name {
		h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return h, capture
}

func submitRemoteDialog(t *testing.T, h *Home) *Home {
	t.Helper()
	model, _ := h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyCtrlS})
	home := model.(*Home)
	if home.newDialog.IsVisible() {
		t.Fatal("submit must close the dialog")
	}
	return home
}

// TestRemoteDialog_SandboxCheckbox_ForwardedToRemoteCreate: with the checkbox
// enabled, the submit handler must hand sandbox=true to the remote-create path
// together with the other dialog values.
func TestRemoteDialog_SandboxCheckbox_ForwardedToRemoteCreate(t *testing.T) {
	h, capture := openRemoteDialogAndTypeName(t, "myserver", "sandboxed-task")
	if h.newDialog.IsSandboxEnabled() {
		t.Fatal("precondition: remote dialog must start with the sandbox checkbox off")
	}
	h.newDialog.ToggleSandbox()
	if !h.newDialog.IsSandboxEnabled() {
		t.Fatal("precondition: ToggleSandbox must enable the checkbox")
	}

	h = submitRemoteDialog(t, h)

	if capture.calls != 1 {
		t.Fatalf("remote create called %d times, want 1", capture.calls)
	}
	if capture.remoteName != "myserver" {
		t.Fatalf("remoteName = %q, want myserver", capture.remoteName)
	}
	if capture.title != "sandboxed-task" {
		t.Fatalf("title = %q, want sandboxed-task", capture.title)
	}
	if !capture.sandbox {
		t.Fatal("sandbox checkbox was enabled in the dialog but not forwarded to the remote create")
	}
	if len(h.instances) != 0 {
		t.Fatalf("local create must not run for remote targets; got %d instances", len(h.instances))
	}
}

// TestRemoteDialog_SandboxUnchecked_NotForwarded: leaving the checkbox off must
// keep the remote create unsandboxed, so existing behaviour is unchanged.
func TestRemoteDialog_SandboxUnchecked_NotForwarded(t *testing.T) {
	h, capture := openRemoteDialogAndTypeName(t, "myserver", "plain-task")

	submitRemoteDialog(t, h)

	if capture.calls != 1 {
		t.Fatalf("remote create called %d times, want 1", capture.calls)
	}
	if capture.sandbox {
		t.Fatal("sandbox must stay off when the checkbox was not enabled")
	}
	if capture.title != "plain-task" {
		t.Fatalf("title = %q, want plain-task", capture.title)
	}
}
