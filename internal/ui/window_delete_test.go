package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

// TestDeleteKey_OnWindowItem_OpensKillWindowConfirm — 'd' over a window
// sub-item opens a kill-window confirmation, mirroring the delete-session
// confirmation shown for session rows.
func TestDeleteKey_OnWindowItem_OpensKillWindowConfirm(t *testing.T) {
	h := newSeamATestHome()
	h.flatItems = []session.Item{
		newRemoveTestItem("id-1", "agent-deck", session.StatusRunning),
		{
			Type:            session.ItemTypeWindow,
			WindowIndex:     2,
			WindowName:      "/agent-deck",
			WindowSessionID: "id-1",
		},
	}
	h.cursor = 1

	newModel, _ := h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	got := newModel.(*Home)

	if !got.confirmDialog.IsVisible() {
		t.Fatalf("confirm dialog should be visible after 'd' on window item")
	}
	if got.confirmDialog.GetConfirmType() != ConfirmKillWindow {
		t.Fatalf("expected ConfirmKillWindow, got %v", got.confirmDialog.GetConfirmType())
	}
	if got.confirmDialog.GetTargetID() != "id-1" {
		t.Fatalf("expected targetID 'id-1', got %q", got.confirmDialog.GetTargetID())
	}
	if got.confirmDialog.GetWindowIndex() != 2 {
		t.Fatalf("expected window index 2, got %d", got.confirmDialog.GetWindowIndex())
	}
}

// TestCuratedFooterWindowItemShowsDelete — the curated footer on a window
// sub-item advertises attach then delete, mirroring session rows now that
// 'd' kills the selected window.
func TestCuratedFooterWindowItemShowsDelete(t *testing.T) {
	home := curatedHome()
	home.flatItems = []session.Item{{
		Type:            session.ItemTypeWindow,
		WindowIndex:     2,
		WindowName:      "/agent-deck",
		WindowSessionID: "id-1",
	}}
	home.cursor = 0

	hints := home.curatedContextHints(home.flatItems[0])
	got := make([]string, len(hints))
	for i, hint := range hints {
		got[i] = hint.label
	}
	want := []string{"attach", "delete"}
	if len(got) != len(want) {
		t.Fatalf("window item context hints = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("window item context hints = %v, want %v", got, want)
		}
	}
}
