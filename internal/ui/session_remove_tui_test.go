package ui

import (
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
