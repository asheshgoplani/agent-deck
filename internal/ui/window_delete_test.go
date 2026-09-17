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

// soleWindowIndexVia returns the index of the (assumed only) window in
// target, without assuming a particular tmux base-index configuration.
func soleWindowIndexVia(t *testing.T, socket, target string) int {
	t.Helper()
	out, err := exec.Command("tmux", "-L", socket, "list-windows", "-t", target, "-F", "#{window_index}").CombinedOutput()
	if err != nil {
		t.Fatalf("list-windows: %v: %s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly one window, got %v", lines)
	}
	var index int
	if _, err := fmt.Sscanf(lines[0], "%d", &index); err != nil {
		t.Fatalf("parse window index %q: %v", lines[0], err)
	}
	return index
}

func windowIndicesVia(t *testing.T, socket, target string) []byte {
	t.Helper()
	out, err := exec.Command("tmux", "-L", socket, "list-windows", "-t", target, "-F", "#{window_index}").CombinedOutput()
	if err != nil {
		t.Fatalf("list-windows: %v: %s", err, out)
	}
	return out
}

// windowIDVia returns the stable tmux window id for the window currently at
// index in target, exactly what a confirm dialog would capture at prompt
// time.
func windowIDVia(t *testing.T, socket, target string, index int) string {
	t.Helper()
	s := &tmux.Session{Name: target, SocketName: socket}
	id, err := s.WindowID(index)
	if err != nil {
		t.Fatalf("WindowID: %v", err)
	}
	return id
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
	onlyIndex := soleWindowIndexVia(t, socket, target)

	windowID := windowIDVia(t, socket, target, onlyIndex)
	home.confirmDialog.ShowKillWindow("kw-1", onlyIndex, "agent", windowID)
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

	windowID := windowIDVia(t, socket, target, newIndex)
	home.confirmDialog.ShowKillWindow("kw-1", newIndex, "shell", windowID)
	_ = home.confirmAction()

	if home.err != nil {
		t.Fatalf("confirmAction with 2 windows should kill without error, got %v", home.err)
	}
	if got := windowCountVia(t, socket, target); got != 1 {
		t.Fatalf("window count = %d, want 1 after kill", got)
	}
}

// TestConfirmKillWindow_RefusesReorderedWindow — the window originally
// selected at an index can close and be replaced by a different window at
// the same index before the user confirms. confirmAction must refuse using
// the window id captured when the dialog opened, never kill by stale
// index/name alone, and leave the replacement window untouched.
func TestConfirmKillWindow_RefusesReorderedWindow(t *testing.T) {
	home, socket, target := armKillWindowHome(t)

	if out, err := exec.Command("tmux", "-L", socket, "new-window", "-t", target, "sleep", "300").CombinedOutput(); err != nil {
		t.Fatalf("new-window: %v: %s", err, out)
	}
	windows := strings.Fields(strings.TrimSpace(string(windowIndicesVia(t, socket, target))))
	if len(windows) != 2 {
		t.Fatalf("setup: window count = %d, want 2", len(windows))
	}
	var staleIndex int
	if _, err := fmt.Sscanf(windows[1], "%d", &staleIndex); err != nil {
		t.Fatalf("parse window index %q: %v", windows[1], err)
	}

	// Capture the id of the window the user is about to select, exactly as
	// the 'd' handler does when it opens the dialog.
	staleWindowID := windowIDVia(t, socket, target, staleIndex)
	home.confirmDialog.ShowKillWindow("kw-1", staleIndex, "shell", staleWindowID)

	// Before the user answers, that window closes and a different window
	// takes its place at the same index.
	if out, err := exec.Command("tmux", "-L", socket, "kill-window", "-t", fmt.Sprintf("%s:%d", target, staleIndex)).CombinedOutput(); err != nil {
		t.Fatalf("kill-window (setup): %v: %s", err, out)
	}
	if out, err := exec.Command("tmux", "-L", socket, "new-window", "-t", fmt.Sprintf("%s:%d", target, staleIndex), "sleep", "300").CombinedOutput(); err != nil {
		t.Fatalf("new-window at stale index (setup): %v: %s", err, out)
	}
	replacementID := windowIDVia(t, socket, target, staleIndex)
	if replacementID == staleWindowID {
		t.Fatalf("setup did not actually replace the window at index %d", staleIndex)
	}

	_ = home.confirmAction()

	if home.err == nil || !strings.Contains(home.err.Error(), "changed") {
		t.Fatalf("confirmAction on a reordered window should refuse with a 'changed' error, got %v", home.err)
	}
	if got := windowCountVia(t, socket, target); got != 2 {
		t.Fatalf("window count after refused kill = %d, want 2", got)
	}
	if got := windowIDVia(t, socket, target, staleIndex); got != replacementID {
		t.Fatalf("window at index %d changed after refused kill: got %s, want %s", staleIndex, got, replacementID)
	}
}

// TestDeleteKey_OnWindowItem_OpensKillWindowConfirm — 'd' over a window
// sub-item opens a kill-window confirmation, mirroring the delete-session
// confirmation shown for session rows. Uses a real tmux-backed instance
// (rather than newSeamATestHome's storage-free stub) because opening the
// dialog now queries live tmux for the window's stable id to arm the
// identity guard — see TestConfirmKillWindow_RefusesReorderedWindow.
func TestDeleteKey_OnWindowItem_OpensKillWindowConfirm(t *testing.T) {
	h, socket, target := armKillWindowHome(t)
	if out, err := exec.Command("tmux", "-L", socket, "new-window", "-t", fmt.Sprintf("%s:2", target)).CombinedOutput(); err != nil {
		t.Fatalf("new-window: %v: %s", err, out)
	}
	wantWindowID := windowIDVia(t, socket, target, 2)

	h.flatItems = []session.Item{
		newRemoveTestItem("kw-1", "agent-deck", session.StatusRunning),
		{
			Type:            session.ItemTypeWindow,
			WindowIndex:     2,
			WindowName:      "/agent-deck",
			WindowSessionID: "kw-1",
		},
	}
	h.cursor = 1

	newModel, _ := h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	got := newModel.(*Home)

	if got.err != nil {
		t.Fatalf("unexpected error opening kill-window confirm: %v", got.err)
	}
	if !got.confirmDialog.IsVisible() {
		t.Fatalf("confirm dialog should be visible after 'd' on window item")
	}
	if got.confirmDialog.GetConfirmType() != ConfirmKillWindow {
		t.Fatalf("expected ConfirmKillWindow, got %v", got.confirmDialog.GetConfirmType())
	}
	if got.confirmDialog.GetTargetID() != "kw-1" {
		t.Fatalf("expected targetID 'kw-1', got %q", got.confirmDialog.GetTargetID())
	}
	if got.confirmDialog.GetWindowIndex() != 2 {
		t.Fatalf("expected window index 2, got %d", got.confirmDialog.GetWindowIndex())
	}
	if got.confirmDialog.GetWindowID() != wantWindowID {
		t.Fatalf("expected captured window id %q (queried live from tmux), got %q", wantWindowID, got.confirmDialog.GetWindowID())
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

// TestConfirmKillWindow_DialogFrame is a golden-frame check on the
// kill-window confirm dialog: it pins the exact rendered text (title,
// window number/name, and the destructive-action details) so a future
// change to the wording or to which fields are shown is a visible diff here
// instead of a silent behavior change. The window id captured for the
// identity guard is intentionally NOT part of the visible frame — it is
// plumbing, not something to show the user.
func TestConfirmKillWindow_DialogFrame(t *testing.T) {
	d := &ConfirmDialog{}
	d.SetSize(80, 24)
	d.ShowKillWindow("kw-1", 2, "agent", "@42")

	frame := d.View()

	wantContains := []string{
		"Kill Window?",
		"This will kill tmux window 2:",
		`"agent"`,
		"Any processes in the window will be killed",
		"Other windows in the session are unaffected",
		"Kill",
		"Cancel",
	}
	for _, want := range wantContains {
		if !strings.Contains(frame, want) {
			t.Errorf("kill-window dialog frame missing %q\n--- got ---\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "@42") {
		t.Errorf("kill-window dialog frame must not leak the internal window id @42 to the user\n--- got ---\n%s", frame)
	}
}
