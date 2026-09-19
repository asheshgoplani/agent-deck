package ui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// killWindowSocketSeq numbers the private tmux socket of each
// armKillWindowHome call so no two tests in this binary share a server.
var killWindowSocketSeq atomic.Int64

// armKillWindowHome builds a Home whose confirm dialog targets a live
// isolated tmux session, so confirmAction's ConfirmKillWindow path can be
// exercised end to end. Returns the home and the socket name.
func armKillWindowHome(t *testing.T) (*Home, string, string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux binary not on PATH; skipping")
	}

	// One socket per test, never one per process: a tmux server keeps its
	// listening socket until it exits, and kill-server returns to its client
	// before that. On a shared name the next test's new-session can still
	// connect to the dying server and fail with "server exited unexpectedly"
	// (v1.16.11 release run, TestConfirmKillWindow_RefusesRenamedWindow).
	socket := fmt.Sprintf("kwg%d-%d", os.Getpid(), killWindowSocketSeq.Add(1))
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

// windowsByIDVia lists target's live windows keyed by stable id.
func windowsByIDVia(t *testing.T, socket, target string) map[string]tmux.WindowInfo {
	t.Helper()
	out, err := exec.Command("tmux", "-L", socket, "list-windows", "-t", target, "-F", "#{window_index} #{window_id} #{window_name}").CombinedOutput()
	if err != nil {
		t.Fatalf("list-windows: %v: %s", err, out)
	}
	wins := map[string]tmux.WindowInfo{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var w tmux.WindowInfo
		if _, err := fmt.Sscanf(line, "%d %s %s", &w.Index, &w.ID, &w.Name); err != nil {
			t.Fatalf("parse list-windows line %q: %v", line, err)
		}
		wins[w.ID] = w
	}
	return wins
}

// newNamedWindowVia adds a named window to target and returns its stable id.
func newNamedWindowVia(t *testing.T, socket, target, name string) string {
	t.Helper()
	out, err := exec.Command("tmux", "-L", socket, "new-window", "-d", "-P", "-F", "#{window_id}", "-t", target, "-n", name, "sleep", "300").CombinedOutput()
	if err != nil {
		t.Fatalf("new-window: %v: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// windowItem is the flat row the TUI builds from one cached WindowInfo.
func windowItem(w tmux.WindowInfo) session.Item {
	return session.Item{
		Type:            session.ItemTypeWindow,
		WindowIndex:     w.Index,
		WindowID:        w.ID,
		WindowName:      w.Name,
		WindowSessionID: "kw-1",
	}
}

// requireRefusalNotice asserts the refusal is shown inside the modal (the
// footer error is clamped away on a full viewport, so it alone is invisible)
// and that it can be dismissed with a key.
func requireRefusalNotice(t *testing.T, home *Home, wantContains ...string) {
	t.Helper()
	if !home.confirmDialog.IsVisible() || home.confirmDialog.GetConfirmType() != ConfirmNotice {
		t.Fatalf("refusal must stay on screen as a notice in the modal, dialog visible=%v type=%v", home.confirmDialog.IsVisible(), home.confirmDialog.GetConfirmType())
	}
	frame := ansi.Strip(home.confirmDialog.View())
	for _, want := range append([]string{"Window Not Killed", "Nothing was closed."}, wantContains...) {
		if !strings.Contains(frame, want) {
			t.Errorf("refusal notice missing %q\n--- got ---\n%s", want, frame)
		}
	}
	newModel, _ := home.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if newModel.(*Home).confirmDialog.IsVisible() {
		t.Fatalf("refusal notice must be dismissable with Esc")
	}
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
// session has only one window left must refuse instead of killing the
// window (which would take the whole session down), and the refusal must be
// visible in the modal. The window row was rendered when 2+ windows existed,
// but the other window can close between rendering and confirmation.
func TestConfirmKillWindow_RefusesLastWindow(t *testing.T) {
	home, socket, target := armKillWindowHome(t)
	onlyIndex := soleWindowIndexVia(t, socket, target)
	if out, err := exec.Command("tmux", "-L", socket, "rename-window", "-t", fmt.Sprintf("%s:%d", target, onlyIndex), "agent").CombinedOutput(); err != nil {
		t.Fatalf("rename-window (setup): %v: %s", err, out)
	}
	var only tmux.WindowInfo
	for _, w := range windowsByIDVia(t, socket, target) {
		only = w
	}

	home.confirmDialog.ShowKillWindow("kw-1", only.Index, only.Name, only.ID)
	_ = home.confirmAction()

	if home.err == nil || !strings.Contains(home.err.Error(), "last window") {
		t.Fatalf("confirmAction on a 1-window session should refuse with a last-window error, got %v", home.err)
	}
	if got := windowCountVia(t, socket, target); got != 1 {
		t.Fatalf("window count = %d, want 1 (the last window must survive)", got)
	}
	requireRefusalNotice(t, home, "last window", only.ID, `"agent"`)
}

// TestConfirmKillWindow_KillsWhenMultipleWindows — with 2+ windows the
// confirmed kill removes exactly the window named in the dialog (by id).
func TestConfirmKillWindow_KillsWhenMultipleWindows(t *testing.T) {
	home, socket, target := armKillWindowHome(t)
	shellID := newNamedWindowVia(t, socket, target, "shell")
	before := windowsByIDVia(t, socket, target)
	if len(before) != 2 {
		t.Fatalf("setup: window count = %d, want 2", len(before))
	}

	home.confirmDialog.ShowKillWindow("kw-1", before[shellID].Index, "shell", shellID)
	_ = home.confirmAction()

	if home.err != nil {
		t.Fatalf("confirmAction with 2 windows should kill without error, got %v", home.err)
	}
	after := windowsByIDVia(t, socket, target)
	if _, alive := after[shellID]; alive || len(after) != 1 {
		t.Fatalf("windows after kill = %v, want exactly one window and %s gone", after, shellID)
	}
	if home.confirmDialog.IsVisible() {
		t.Fatalf("dialog must close after a successful kill")
	}
}

// TestConfirmKillWindow_RefusesClosedWindow — the window originally
// selected can close and be replaced by a different window at the same
// index before the user confirms. confirmAction must refuse using the
// window id captured when the dialog opened, never kill by stale index/name
// alone, leave the replacement window untouched, and show the refusal.
func TestConfirmKillWindow_RefusesClosedWindow(t *testing.T) {
	home, socket, target := armKillWindowHome(t)
	staleID := newNamedWindowVia(t, socket, target, "shell")
	staleIndex := windowsByIDVia(t, socket, target)[staleID].Index

	// The row the user saw, exactly as the 'd' handler captures it.
	home.confirmDialog.ShowKillWindow("kw-1", staleIndex, "shell", staleID)

	// Before the user answers, that window closes and a different window
	// with the same name takes its place at the same index.
	if out, err := exec.Command("tmux", "-L", socket, "kill-window", "-t", target+":"+staleID).CombinedOutput(); err != nil {
		t.Fatalf("kill-window (setup): %v: %s", err, out)
	}
	if out, err := exec.Command("tmux", "-L", socket, "new-window", "-d", "-t", fmt.Sprintf("%s:%d", target, staleIndex), "-n", "shell", "sleep", "300").CombinedOutput(); err != nil {
		t.Fatalf("new-window at stale index (setup): %v: %s", err, out)
	}
	replacementID := ""
	for id, w := range windowsByIDVia(t, socket, target) {
		if w.Index == staleIndex {
			replacementID = id
		}
	}
	if replacementID == "" || replacementID == staleID {
		t.Fatalf("setup did not actually replace the window at index %d", staleIndex)
	}

	_ = home.confirmAction()

	if home.err == nil || !strings.Contains(home.err.Error(), "changed") {
		t.Fatalf("confirmAction on a replaced window should refuse with a 'changed' error, got %v", home.err)
	}
	after := windowsByIDVia(t, socket, target)
	if _, alive := after[replacementID]; !alive || len(after) != 2 {
		t.Fatalf("windows after refused kill = %v, want 2 with %s alive", after, replacementID)
	}
	requireRefusalNotice(t, home, "changed", "refusing", staleID)
}

// TestConfirmKillWindow_RefusesRenamedWindow — the selected window is still
// there (same id) but was renamed between prompt and confirm, so it is no
// longer what the user read in the dialog: refuse, leave it alone, show why.
func TestConfirmKillWindow_RefusesRenamedWindow(t *testing.T) {
	home, socket, target := armKillWindowHome(t)
	id := newNamedWindowVia(t, socket, target, "before")
	index := windowsByIDVia(t, socket, target)[id].Index

	home.confirmDialog.ShowKillWindow("kw-1", index, "before", id)
	if out, err := exec.Command("tmux", "-L", socket, "rename-window", "-t", target+":"+id, "after").CombinedOutput(); err != nil {
		t.Fatalf("rename-window (setup): %v: %s", err, out)
	}

	_ = home.confirmAction()

	if home.err == nil || !strings.Contains(home.err.Error(), "changed") {
		t.Fatalf("confirmAction on a renamed window should refuse with a 'changed' error, got %v", home.err)
	}
	after := windowsByIDVia(t, socket, target)
	if w, alive := after[id]; !alive || w.Name != "after" || len(after) != 2 {
		t.Fatalf("windows after refused kill = %v, want 2 with %s alive as \"after\"", after, id)
	}
	requireRefusalNotice(t, home, "changed", "refusing", id, `"before"`)
}

// TestDeleteKey_OnWindowItem_OpensKillWindowConfirm — 'd' over a window
// sub-item opens a kill-window confirmation carrying the id and name of that
// cached row, without a live lookup by index. The window cache can be up to
// a tick stale, so the live window at the row's index may already be a
// different one; the dialog must name the window the user read on screen.
func TestDeleteKey_OnWindowItem_OpensKillWindowConfirm(t *testing.T) {
	h, _, _ := armKillWindowHome(t)

	cached := tmux.WindowInfo{Index: 2, ID: "@1234", Name: "/agent-deck"}
	h.flatItems = []session.Item{
		newRemoveTestItem("kw-1", "agent-deck", session.StatusRunning),
		windowItem(cached),
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
	if got.confirmDialog.GetWindowIndex() != cached.Index || got.confirmDialog.GetWindowID() != cached.ID || got.confirmDialog.GetWindowName() != cached.Name {
		t.Fatalf("dialog must carry the cached row's index/id/name %+v, got %d/%q/%q", cached,
			got.confirmDialog.GetWindowIndex(), got.confirmDialog.GetWindowID(), got.confirmDialog.GetWindowName())
	}
	frame := ansi.Strip(got.confirmDialog.View())
	for _, want := range []string{"@1234", `"/agent-deck"`} {
		if !strings.Contains(frame, want) {
			t.Errorf("dialog must show the window id and name so the user confirms a specific window; missing %q\n--- got ---\n%s", want, frame)
		}
	}
}

// TestDeleteKey_StaleRow_RefusesAndSparesLiveWindow reproduces the wrong
// window kill from review: rows [1] third-shell, [2] fourth-shell were
// rendered, then outside agent-deck window :1 closed and tmux renumbered, so
// fourth-shell is now live at index 1 while the cache (up to a tick stale)
// still shows third-shell there. 'd' on the stale row must name third-shell
// (its id), and 'y' must refuse (that id is gone) and leave fourth-shell
// alive, instead of killing whatever now sits at index 1.
func TestDeleteKey_StaleRow_RefusesAndSparesLiveWindow(t *testing.T) {
	h, socket, target := armKillWindowHome(t)
	secondID := newNamedWindowVia(t, socket, target, "second-shell")
	thirdID := newNamedWindowVia(t, socket, target, "third-shell")
	fourthID := newNamedWindowVia(t, socket, target, "fourth-shell")
	rendered := windowsByIDVia(t, socket, target)

	h.flatItems = []session.Item{
		newRemoveTestItem("kw-1", "agent-deck", session.StatusRunning),
		windowItem(rendered[secondID]),
		windowItem(rendered[thirdID]),
		windowItem(rendered[fourthID]),
	}
	h.cursor = 2 // the third-shell row

	// Between render and 'd': second-shell closes and tmux renumbers.
	if out, err := exec.Command("tmux", "-L", socket, "kill-window", "-t", target+":"+secondID).CombinedOutput(); err != nil {
		t.Fatalf("kill-window (setup): %v: %s", err, out)
	}
	if out, err := exec.Command("tmux", "-L", socket, "move-window", "-r", "-t", target).CombinedOutput(); err != nil {
		t.Fatalf("move-window -r (setup): %v: %s", err, out)
	}
	live := windowsByIDVia(t, socket, target)
	if live[thirdID].Index != rendered[secondID].Index || live[fourthID].Index != rendered[thirdID].Index {
		t.Fatalf("setup: expected renumbering to shift third/fourth down one index, got %v", live)
	}

	newModel, _ := h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	h = newModel.(*Home)
	if h.err != nil || !h.confirmDialog.IsVisible() || h.confirmDialog.GetConfirmType() != ConfirmKillWindow {
		t.Fatalf("'d' on the stale row must open the kill confirm, err=%v visible=%v", h.err, h.confirmDialog.IsVisible())
	}
	if h.confirmDialog.GetWindowID() != thirdID || h.confirmDialog.GetWindowName() != "third-shell" {
		t.Fatalf("dialog must name the row the user read (%s third-shell), got %s %q", thirdID, h.confirmDialog.GetWindowID(), h.confirmDialog.GetWindowName())
	}

	// Between 'd' and 'y': third-shell itself closes too and tmux renumbers
	// again, so the id the dialog carries no longer exists.
	if out, err := exec.Command("tmux", "-L", socket, "kill-window", "-t", target+":"+thirdID).CombinedOutput(); err != nil {
		t.Fatalf("kill-window (setup): %v: %s", err, out)
	}
	if out, err := exec.Command("tmux", "-L", socket, "move-window", "-r", "-t", target).CombinedOutput(); err != nil {
		t.Fatalf("move-window -r (setup): %v: %s", err, out)
	}
	if _, alive := windowsByIDVia(t, socket, target)[fourthID]; !alive {
		t.Fatalf("setup: fourth-shell must still be alive after renumbering")
	}

	newModel, _ = h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	h = newModel.(*Home)

	if h.err == nil || !strings.Contains(h.err.Error(), "changed") {
		t.Fatalf("'y' on a window that is gone must refuse with a 'changed' error, got %v", h.err)
	}
	after := windowsByIDVia(t, socket, target)
	if w, alive := after[fourthID]; !alive || w.Name != "fourth-shell" {
		t.Fatalf("fourth-shell (%s) was killed in place of third-shell: %v", fourthID, after)
	}
	requireRefusalNotice(t, h, thirdID, `"third-shell"`)
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

// TestConfirmKillWindow_DialogFrame checks the kill-window confirm dialog
// names a specific window: index, stable id and name, plus the
// destructive-action details, so the user confirms exactly one window and
// the id can be matched against `session window close --json`.
func TestConfirmKillWindow_DialogFrame(t *testing.T) {
	d := &ConfirmDialog{}
	d.SetSize(80, 24)
	d.ShowKillWindow("kw-1", 2, "agent", "@42")

	frame := d.View()

	wantContains := []string{
		"Kill Window?",
		"This will kill tmux window 2 (@42):",
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
}
