package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

// newRemoveTestItem builds a flatItems entry for Seam A tests with a given status.
func newRemoveTestItem(id, title string, status session.Status) session.Item {
	return session.Item{
		Type: session.ItemTypeSession,
		Session: &session.Instance{
			ID:     id,
			Title:  title,
			Status: status,
		},
	}
}

// TestSessionRemoveTUI_CapitalX_OnStopped_OpensConfirm — 'X' over a stopped
// session opens the remove-confirm dialog (distinct from the existing 'd'
// destructive-delete dialog).
func TestSessionRemoveTUI_CapitalX_OnStopped_OpensConfirm(t *testing.T) {
	h := newSeamATestHome()
	h.flatItems = []session.Item{newRemoveTestItem("id-1", "stopped-one", session.StatusStopped)}
	h.cursor = 0

	newModel, _ := h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	got := newModel.(*Home)

	if !got.confirmDialog.IsVisible() {
		t.Fatalf("confirm dialog should be visible after 'X' on stopped session")
	}
	if got.confirmDialog.GetConfirmType() != ConfirmRemoveSession {
		t.Fatalf("expected ConfirmRemoveSession, got %v", got.confirmDialog.GetConfirmType())
	}
	if got.confirmDialog.GetTargetID() != "id-1" {
		t.Fatalf("expected targetID 'id-1', got %q", got.confirmDialog.GetTargetID())
	}
}

// TestSessionRemoveTUI_CapitalX_OnErrored_OpensConfirm — error state qualifies.
func TestSessionRemoveTUI_CapitalX_OnErrored_OpensConfirm(t *testing.T) {
	h := newSeamATestHome()
	h.flatItems = []session.Item{newRemoveTestItem("id-err", "err-one", session.StatusError)}
	h.cursor = 0

	newModel, _ := h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	got := newModel.(*Home)

	if !got.confirmDialog.IsVisible() {
		t.Fatalf("confirm dialog should be visible after 'X' on errored session")
	}
	if got.confirmDialog.GetConfirmType() != ConfirmRemoveSession {
		t.Fatalf("expected ConfirmRemoveSession, got %v", got.confirmDialog.GetConfirmType())
	}
}

// TestSessionRemoveTUI_CapitalX_OnRunning_ShowsError — safety gate in the UI.
func TestSessionRemoveTUI_CapitalX_OnRunning_ShowsError(t *testing.T) {
	h := newSeamATestHome()
	h.flatItems = []session.Item{newRemoveTestItem("id-run", "running-one", session.StatusRunning)}
	h.cursor = 0

	newModel, _ := h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	got := newModel.(*Home)

	if got.confirmDialog.IsVisible() {
		t.Fatalf("confirm dialog should NOT open for a running session")
	}
	if got.err == nil {
		t.Fatalf("expected an error message steering user to 'd' for destructive delete")
	}
}

// TestSessionRemoveTUI_CtrlX_OpensBulkConfirmWithCount — Ctrl+X routes to
// the bulk-errored dialog and passes the correct count.
func TestSessionRemoveTUI_CtrlX_OpensBulkConfirmWithCount(t *testing.T) {
	h := newSeamATestHome()
	h.instances = []*session.Instance{
		{ID: "e1", Title: "err-1", Status: session.StatusError},
		{ID: "e2", Title: "err-2", Status: session.StatusError},
		{ID: "ok", Title: "running", Status: session.StatusRunning},
	}

	newModel, _ := h.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	got := newModel.(*Home)

	if !got.confirmDialog.IsVisible() {
		t.Fatalf("confirm dialog should be visible after Ctrl+X")
	}
	if got.confirmDialog.GetConfirmType() != ConfirmBulkRemoveErrored {
		t.Fatalf("expected ConfirmBulkRemoveErrored, got %v", got.confirmDialog.GetConfirmType())
	}
	// mcpCount is reused by the dialog as a generic integer carrier for the bulk count.
	if got.confirmDialog.mcpCount != 2 {
		t.Fatalf("expected bulk count 2, got %d", got.confirmDialog.mcpCount)
	}
}

// TestSessionRemoveTUI_CtrlX_NoErrored_ShowsError — empty-set guard.
func TestSessionRemoveTUI_CtrlX_NoErrored_ShowsError(t *testing.T) {
	h := newSeamATestHome()
	h.instances = []*session.Instance{
		{ID: "ok", Title: "idle-one", Status: session.StatusIdle},
	}

	newModel, _ := h.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	got := newModel.(*Home)

	if got.confirmDialog.IsVisible() {
		t.Fatalf("confirm dialog should NOT open when there are no errored sessions")
	}
	if got.err == nil {
		t.Fatalf("expected an error message when no errored sessions exist")
	}
}

// newRemoveRemoteTestItem builds a flatItems entry for a session on a remote
// deck, with the status the remote reported for it.
func newRemoveRemoteTestItem(id, title, remoteName, status string) session.Item {
	return session.Item{
		Type: session.ItemTypeRemoteSession,
		RemoteSession: &session.RemoteSessionInfo{
			ID:     id,
			Title:  title,
			Status: status,
		},
		RemoteName: remoteName,
	}
}

// TestSessionRemoveTUI_CapitalX_OnStoppedRemote_OpensConfirm — 'X' over a
// stopped session on a remote deck opens the remote remove-confirm dialog.
// Regression: this keypress used to fall through with no branch for remote
// rows, so it did nothing at all and never said why.
func TestSessionRemoveTUI_CapitalX_OnStoppedRemote_OpensConfirm(t *testing.T) {
	h := newSeamATestHome()
	h.flatItems = []session.Item{newRemoveRemoteTestItem("rid-1", "remote-stopped", "box", string(session.StatusStopped))}
	h.cursor = 0

	newModel, _ := h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	got := newModel.(*Home)

	if !got.confirmDialog.IsVisible() {
		t.Fatalf("confirm dialog should be visible after 'X' on a stopped remote session")
	}
	if got.confirmDialog.GetConfirmType() != ConfirmRemoveRemoteSession {
		t.Fatalf("expected ConfirmRemoveRemoteSession, got %v", got.confirmDialog.GetConfirmType())
	}
	if got.confirmDialog.GetTargetID() != "rid-1" {
		t.Fatalf("expected targetID 'rid-1', got %q", got.confirmDialog.GetTargetID())
	}
	if got.confirmDialog.GetRemoteName() != "box" {
		t.Fatalf("expected remote 'box', got %q", got.confirmDialog.GetRemoteName())
	}
}

// TestSessionRemoveTUI_CapitalX_OnErroredRemote_OpensConfirm — error state
// qualifies on a remote too.
func TestSessionRemoveTUI_CapitalX_OnErroredRemote_OpensConfirm(t *testing.T) {
	h := newSeamATestHome()
	h.flatItems = []session.Item{newRemoveRemoteTestItem("rid-err", "remote-err", "box", string(session.StatusError))}
	h.cursor = 0

	newModel, _ := h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	got := newModel.(*Home)

	if !got.confirmDialog.IsVisible() {
		t.Fatalf("confirm dialog should be visible after 'X' on an errored remote session")
	}
	if got.confirmDialog.GetConfirmType() != ConfirmRemoveRemoteSession {
		t.Fatalf("expected ConfirmRemoveRemoteSession, got %v", got.confirmDialog.GetConfirmType())
	}
}

// TestSessionRemoveTUI_CapitalX_OnRunningRemote_ShowsError — the gate holds on
// a remote row and, unlike before, it actually says something.
func TestSessionRemoveTUI_CapitalX_OnRunningRemote_ShowsError(t *testing.T) {
	h := newSeamATestHome()
	h.flatItems = []session.Item{newRemoveRemoteTestItem("rid-run", "remote-running", "box", string(session.StatusRunning))}
	h.cursor = 0

	newModel, _ := h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	got := newModel.(*Home)

	if got.confirmDialog.IsVisible() {
		t.Fatalf("confirm dialog should NOT open for a running remote session")
	}
	if got.err == nil {
		t.Fatalf("expected an error steering the user to 'd' for a running remote session")
	}
}

// TestSessionRemoveTUI_ConfirmRemoteRemove_RunsRemoteSessionRemove — the
// confirmation routes to the remote's own `session remove`, not to the
// destructive `remove`.
func TestSessionRemoveTUI_ConfirmRemoteRemove_RunsRemoteSessionRemove(t *testing.T) {
	h := newSeamATestHome()
	h.confirmDialog.ShowRemoveRemoteSession("box", "rid-1", "remote-stopped")

	newModel, cmd := h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	got := newModel.(*Home)

	if got.confirmDialog.IsVisible() {
		t.Fatalf("confirm dialog should close after 'y'")
	}
	if cmd == nil {
		t.Fatalf("expected a command that performs the remote remove")
	}
	// The command runs against the configured remote, so an unconfigured
	// remote must surface an error rather than panic when it executes.
	msg, ok := cmd().(remoteSessionRemovedMsg)
	if !ok {
		t.Fatalf("expected remoteSessionRemovedMsg, got %T", cmd())
	}
	if msg.sessionID != "rid-1" || msg.remoteName != "box" {
		t.Fatalf("unexpected msg: %+v", msg)
	}
	if msg.err == nil {
		t.Fatalf("expected an error for an unconfigured remote %q", "box")
	}
}

// TestRemoteSessionRemoved_SuccessDropsRow — a confirmed registry remove drops
// the row at once and asks the remote for its listing, so the controller never
// keeps a row the remote no longer has.
func TestRemoteSessionRemoved_SuccessDropsRow(t *testing.T) {
	home := newTestHomeWithItems(100, 30, nil)
	home.remoteSessions = map[string][]session.RemoteSessionInfo{
		"box": {remoteInfo("box", "rid-1", "doomed", string(session.StatusStopped)), remoteInfo("box", "rid-2", "kept", string(session.StatusStopped))},
	}
	home.setRemotePending("rid-1", "removing…")

	model, cmd := home.Update(remoteSessionRemovedMsg{remoteName: "box", sessionID: "rid-1", title: "doomed"})
	h := model.(*Home)

	if got := remoteTitles(h, "box"); len(got) != 1 || got[0] != "kept" {
		t.Fatalf("a confirmed remove must drop the row; got %v", got)
	}
	if _, pending := h.remotePending["rid-1"]; pending {
		t.Fatal("the pending marker must be cleared")
	}
	if cmd == nil {
		t.Fatal("a confirmed remove must refetch the remote's listing")
	}
	if h.err == nil || !strings.Contains(h.err.Error(), "removed 'doomed' from the registry on box") {
		t.Fatalf("footer = %v", h.err)
	}
}

// TestRemoteSessionRemoved_ErrorKeepsRow — a refusal (the remote's own status
// gate firing between the keypress and the call) keeps the row and says why,
// because nothing was removed.
func TestRemoteSessionRemoved_ErrorKeepsRow(t *testing.T) {
	home := newTestHomeWithItems(100, 30, nil)
	home.remoteSessions = map[string][]session.RemoteSessionInfo{
		"box": {remoteInfo("box", "rid-1", "went-live", string(session.StatusRunning))},
	}
	home.setRemotePending("rid-1", "removing…")

	refusal := errors.New("session 'went-live' is in state 'running'; only stopped/error sessions may be removed without --force")
	model, _ := home.Update(remoteSessionRemovedMsg{remoteName: "box", sessionID: "rid-1", title: "went-live", err: refusal})
	h := model.(*Home)

	if got := remoteTitles(h, "box"); len(got) != 1 || got[0] != "went-live" {
		t.Fatalf("a refused remove must keep the row; got %v", got)
	}
	if _, pending := h.remotePending["rid-1"]; pending {
		t.Fatal("the pending marker must be cleared")
	}
	if h.err == nil || !strings.Contains(h.err.Error(), "failed to remove 'went-live' on box") {
		t.Fatalf("footer = %v", h.err)
	}
}

// TestRemoteSessionRemoved_InterruptKeepsRow — a reply lost with the channel
// (#3) leaves the outcome unknown: the row stays, the footer says so, and a
// fetch decides.
func TestRemoteSessionRemoved_InterruptKeepsRow(t *testing.T) {
	interrupted := fmt.Errorf("ssh wrapper: %w", session.ErrRemoteInterrupted)
	home := newTestHomeWithItems(100, 30, nil)
	home.remoteSessions = map[string][]session.RemoteSessionInfo{
		"box": {remoteInfo("box", "rid-1", "maybe-gone", string(session.StatusStopped))},
	}
	home.setRemotePending("rid-1", "removing…")

	model, cmd := home.Update(remoteSessionRemovedMsg{remoteName: "box", sessionID: "rid-1", title: "maybe-gone", err: interrupted})
	h := model.(*Home)

	if got := remoteTitles(h, "box"); len(got) != 1 || got[0] != "maybe-gone" {
		t.Fatalf("an interrupted remove must keep the row until the fetch decides; got %v", got)
	}
	if cmd == nil {
		t.Fatal("an interrupted remove must start a fetch")
	}
	if h.err == nil || !strings.Contains(h.err.Error(), "connection dropped before the reply, refreshing") {
		t.Fatalf("footer = %v", h.err)
	}
}
