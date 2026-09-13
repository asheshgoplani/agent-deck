package ui

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// A remote create whose path the server reports missing must not end as a
// bare error in the footer: the TUI asks whether to create the directory on
// that remote and, only on confirmation, retries with CreateDir set so the
// server's `add --create-dir` makes it.

func newRemoteDirNeededHome(t *testing.T) (*Home, *remoteCreateCapture) {
	t.Helper()
	setXDGTestHome(t)
	h := newTestHomeWithItems(100, 30, nil)
	capture := &remoteCreateCapture{}
	h.remoteCreateSink = capture.sink
	return h, capture
}

func missingDirMsg() remoteCreateDirNeededMsg {
	return remoteCreateDirNeededMsg{
		remoteName: "lab",
		opts:       session.RemoteAddOptions{Tool: "claude", Title: "new work", Path: "/srv/new-project"},
		err:        errors.New("ssh command failed: exit status 1: Error: path does not exist: /srv/new-project"),
	}
}

func TestRemoteCreateMissingDir_ConfirmRetriesWithCreateDir(t *testing.T) {
	h, capture := newRemoteDirNeededHome(t)

	model, _ := h.Update(missingDirMsg())
	h = model.(*Home)
	if !h.confirmDialog.IsVisible() || h.confirmDialog.GetConfirmType() != ConfirmCreateDirectory {
		t.Fatalf("missing remote path must open the create-directory confirmation, got visible=%v type=%v",
			h.confirmDialog.IsVisible(), h.confirmDialog.GetConfirmType())
	}
	if got := h.confirmDialog.GetRemoteName(); got != "lab" {
		t.Fatalf("confirmation remote = %q, want lab", got)
	}
	if capture.calls != 0 {
		t.Fatalf("remote contacted %d times before the user confirmed, want 0", capture.calls)
	}

	model, _ = h.handleConfirmDialogKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	h = model.(*Home)
	if capture.calls != 1 {
		t.Fatalf("remote create calls after confirm = %d, want 1", capture.calls)
	}
	if capture.remoteName != "lab" || !capture.opts.CreateDir || capture.opts.Path != "/srv/new-project" || capture.opts.Title != "new work" {
		t.Fatalf("retry forwarded remote=%q opts=%+v; want the same dialog values on lab with CreateDir", capture.remoteName, capture.opts)
	}
	if h.confirmDialog.IsVisible() || h.pendingRemoteCreate != nil {
		t.Fatal("confirmation must close and clear the pending create after retrying")
	}
}

func TestRemoteCreateMissingDir_CancelCreatesNothing(t *testing.T) {
	h, capture := newRemoteDirNeededHome(t)

	model, _ := h.Update(missingDirMsg())
	h = model.(*Home)
	model, _ = h.handleConfirmDialogKey(tea.KeyMsg{Type: tea.KeyEsc})
	h = model.(*Home)
	if capture.calls != 0 {
		t.Fatalf("remote contacted %d times after cancel, want 0", capture.calls)
	}
	if h.confirmDialog.IsVisible() || h.pendingRemoteCreate != nil {
		t.Fatal("cancel must close the confirmation and drop the pending create")
	}
}
