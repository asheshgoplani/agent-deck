package ui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	tea "github.com/charmbracelet/bubbletea"
)

// armKillWindowHome builds a Home whose confirm dialog targets a live
// isolated tmux session, so confirmAction's ConfirmKillWindow path can be
// exercised end to end. Returns the home and the socket name.
func armKillWindowHome(t *testing.T) (*Home, string, string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux binary not on PATH; skipping")
	}

	socket := fmt.Sprintf("kwg%d", os.Getpid())
	target := "agentdeck_kwguard"
	if out, err := exec.Command("tmux", "-L", socket, "new-session", "-d", "-x", "80", "-y", "24", "-s", target, "sleep", "300").CombinedOutput(); err != nil {
		t.Fatalf("create tmux session: %v: %s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	})

	inst := &session.Instance{ID: "kw-1", Title: "kw", Status: session.StatusRunning}
	inst.SetTmuxSessionForTest(&tmux.Session{Name: target, SocketName: socket})

	home := NewHome()
	home.width = 120
	home.height = 40
	home.initialLoading = false
	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instanceByID = map[string]*session.Instance{inst.ID: inst}
	home.instancesMu.Unlock()

	return home, socket, target
}

func windowCountVia(t *testing.T, socket, target string) int {
	t.Helper()
	out, err := exec.Command("tmux", "-L", socket, "list-windows", "-t", target, "-F", "#{window_index}").CombinedOutput()
	if err != nil {
		t.Fatalf("list-windows: %v: %s", err, out)
	}
	return len(strings.Split(strings.TrimSpace(string(out)), "\n"))
}

// TestKillWindow_RemoteSessionNotApplicable documents that the kill-window
// flow is intentionally local-only. Window sub-items (ItemTypeWindow) are
// injected under local sessions from the local tmux window cache
// (GetCachedWindows in rebuildFlatItems); ItemTypeRemoteSession rows never
// grow window sub-items, so the 'd' handler's ItemTypeWindow branch is
// unreachable for remote sessions. If remote window listing is ever added,
// this skip should be replaced with real RemoteSession coverage.
func TestKillWindow_RemoteSessionNotApplicable(t *testing.T) {
	t.Skip("kill-window is local-only by design: remote sessions have no window sub-items to select")
}

// TestConfirmKillWindow_RefusesLastWindow — confirming a kill when the
// session has only one window left must refuse with an error instead of
// killing the window (which would take the whole session down). The window
// row was rendered when 2+ windows existed, but the other window can close
// between rendering and confirmation.
func TestConfirmKillWindow_RefusesLastWindow(t *testing.T) {
	home, socket, target := armKillWindowHome(t)

	home.confirmDialog.ShowKillWindow("kw-1", 1, "agent")
	_ = home.confirmAction()

	if home.err == nil || !strings.Contains(home.err.Error(), "last window") {
		t.Fatalf("confirmAction on a 1-window session should refuse with a last-window error, got %v", home.err)
	}
	if got := windowCountVia(t, socket, target); got != 1 {
		t.Fatalf("window count = %d, want 1 (the last window must survive)", got)
	}
}

// TestConfirmKillWindow_KillsWhenMultipleWindows — with 2+ windows the
// confirmed kill removes exactly the targeted window.
func TestConfirmKillWindow_KillsWhenMultipleWindows(t *testing.T) {
	home, socket, target := armKillWindowHome(t)

	if out, err := exec.Command("tmux", "-L", socket, "new-window", "-t", target, "sleep", "300").CombinedOutput(); err != nil {
		t.Fatalf("new-window: %v: %s", err, out)
	}
	out, err := exec.Command("tmux", "-L", socket, "list-windows", "-t", target, "-F", "#{window_index}").CombinedOutput()
	if err != nil {
		t.Fatalf("list-windows: %v: %s", err, out)
	}
	windows := strings.Fields(strings.TrimSpace(string(out)))
	if len(windows) != 2 {
		t.Fatalf("setup: window count = %d, want 2", len(windows))
	}

	var newIndex int
	if _, err := fmt.Sscanf(windows[1], "%d", &newIndex); err != nil {
		t.Fatalf("parse window index %q: %v", windows[1], err)
	}

	home.confirmDialog.ShowKillWindow("kw-1", newIndex, "shell")
	_ = home.confirmAction()

	if home.err != nil {
		t.Fatalf("confirmAction with 2 windows should kill without error, got %v", home.err)
	}
	if got := windowCountVia(t, socket, target); got != 1 {
		t.Fatalf("window count = %d, want 1 after kill", got)
	}
}

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
