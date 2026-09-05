package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// Enter treats window and remote rows as embedded targets. A mouse user
// double-clicking the same row must land in the same place; before this the
// event fell through the session-only branch and did nothing.

func doubleClickRow(t *testing.T, home *Home, y int) *Home {
	t.Helper()
	click := tea.MouseMsg{X: 5, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
	model, _ := home.Update(click)
	model, _ = model.(*Home).Update(click)
	return model.(*Home)
}

func TestEmbeddedDoubleClickActivatesRemoteRow(t *testing.T) {
	remote := &session.RemoteSessionInfo{ID: "remote-1", Title: "lab-claude", Status: "running", Tool: "claude"}
	items := []session.Item{
		{Type: session.ItemTypeRemoteSession, RemoteSession: remote, RemoteName: "lab", Level: 1},
	}
	home := newTestHomeWithItems(100, 30, items)
	home.embeddedLayout = true
	home.insertOpenKeySender = func(insertTargetRef) (insertKeySender, error) {
		return &fakeInsertKeySender{}, nil
	}

	home = doubleClickRow(t, home, home.getListContentStartY())

	if !home.embeddedMode {
		t.Fatalf("double-click on a remote row did not enter embedded mode (err=%v)", home.err)
	}
	if home.insertModeRemoteName != "lab" || home.insertModeRemoteID != "remote-1" {
		t.Fatalf("embedded target = %q/%q, want lab/remote-1", home.insertModeRemoteName, home.insertModeRemoteID)
	}
}

func TestEmbeddedDoubleClickActivatesWindowRow(t *testing.T) {
	inst := session.NewInstanceWithTool("parent", "/tmp/parent", "claude")
	inst.SetTmuxSessionForTest(tmux.NewSession("parent", "/tmp/parent"))
	items := []session.Item{
		{Type: session.ItemTypeSession, Session: inst, Level: 1},
		{Type: session.ItemTypeWindow, WindowSessionID: inst.ID, WindowIndex: 2, Level: 2},
	}
	home := newTestHomeWithItems(100, 30, items)
	home.embeddedLayout = true
	home.instances = []*session.Instance{inst}
	home.instanceByID = map[string]*session.Instance{inst.ID: inst}
	home.sessionExists = func(*session.Instance) bool { return true }
	home.insertOpenKeySender = func(insertTargetRef) (insertKeySender, error) {
		return &fakeInsertKeySender{}, nil
	}

	// The window row sits under the two-line session card.
	rowY := home.getListContentStartY() + home.sidebarItemRenderHeightAtWidth(items[0], home.sessionsPaneWidth())
	home = doubleClickRow(t, home, rowY)

	if !home.embeddedMode {
		t.Fatalf("double-click on a window row did not enter embedded mode (err=%v)", home.err)
	}
	if home.insertModeSessionID != inst.ID {
		t.Fatalf("embedded target = %q, want the window's parent session %q", home.insertModeSessionID, inst.ID)
	}
	if home.cursor != 1 {
		t.Fatalf("cursor = %d, want the double-clicked window row", home.cursor)
	}
}

// The page keys used to move the cursor by a line count. With two- and
// three-line cards that skipped whole visual pages; a page step must charge
// each row what it draws.
func TestPageStepItemsChargesRenderedRowHeight(t *testing.T) {
	items := make([]session.Item, 20)
	for i := range items {
		items[i] = session.Item{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s"}}
	}
	home := newTestHomeWithItems(120, 40, items)
	home.embeddedLayout = true
	home.sidebarDensity = session.SidebarDensityFull

	if got := home.pageStepItems(9, 1); got != 3 {
		t.Fatalf("full density: 9 lines forward = %d rows, want 3", got)
	}
	home.cursor = 19
	if got := home.pageStepItems(9, -1); got != 3 {
		t.Fatalf("full density: 9 lines backward = %d rows, want 3", got)
	}

	home.cursor = 0
	home.sidebarDensity = session.SidebarDensityMinimal
	if got := home.pageStepItems(9, 1); got != 9 {
		t.Fatalf("minimal density: 9 lines = %d rows, want 9", got)
	}

	// A budget smaller than one card still moves one row, never zero.
	home.sidebarDensity = session.SidebarDensityFull
	if got := home.pageStepItems(1, 1); got != 1 {
		t.Fatalf("tiny budget = %d rows, want 1", got)
	}

	// The classic layout keeps the historical lines-as-rows arithmetic.
	home.embeddedLayout = false
	if got := home.pageStepItems(9, 1); got != 9 {
		t.Fatalf("classic layout: 9 lines = %d rows, want 9", got)
	}
}

// The remote card's second line is a density decision. Gating it on the
// presence of a tool marker made a tool-less remote row two lines tall at
// minimal density while the measurement said one, so clicks below it landed
// on the wrong session.
func TestEmbeddedRemoteRowMinimalDensityIsOneLineWithoutTool(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30
	home.embeddedLayout = true
	home.sidebarDensity = session.SidebarDensityMinimal

	item := session.Item{
		Type:          session.ItemTypeRemoteSession,
		RemoteSession: &session.RemoteSessionInfo{ID: "r1", Title: "no-tool", Status: "running"},
		RemoteName:    "lab",
		Level:         1,
	}
	var b strings.Builder
	home.renderRemoteSessionItemAtWidth(&b, item, false, 48)
	lines := strings.Split(strings.TrimSuffix(ansi.Strip(b.String()), "\n"), "\n")
	if want := home.sidebarItemRenderHeightAtWidth(item, 48); len(lines) != want {
		t.Fatalf("rendered %d lines, measured %d:\n%s", len(lines), want, b.String())
	}
	if len(lines) != 1 {
		t.Fatalf("minimal density drew %d lines, want 1:\n%s", len(lines), b.String())
	}
}

// The embedded row reads its label from the render snapshot like the classic
// row: no per-row Instance lock in View(), and auto-named sessions show their
// task description instead of the generated handle.
func TestEmbeddedSessionRowUsesSnapshotLabel(t *testing.T) {
	home, inst, _ := armHomeWithOneSession(t)
	home.embeddedLayout = true
	state := sessionRenderState{
		status:    session.StatusRunning,
		tool:      "claude",
		title:     "quick-a1b2",
		autoName:  true,
		paneTitle: "Fix the login redirect",
	}

	var b strings.Builder
	home.renderEmbeddedSessionItem(&b, session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1}, false, state, 48)
	row := ansi.Strip(b.String())
	if !strings.Contains(row, "Fix the login redirect") {
		t.Fatalf("auto-named row did not promote the task description:\n%s", row)
	}
	if strings.Contains(row, "quick-a1b2") {
		t.Fatalf("auto-named row still shows the generated handle:\n%s", row)
	}
}
