package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestSidebarDefaultsGroupedAndAltVTogglesFlat(t *testing.T) {
	home, _ := buildTwoGroupHome(t)
	home.embeddedLayout = true
	home.storage = nil
	if home.sidebarMode != sidebarGrouped {
		t.Fatalf("initial sidebar mode = %v, want grouped", home.sidebarMode)
	}
	home.groupTree.CollapseGroup("alpha")
	home.groupTree.CollapseGroup("beta")
	home.rebuildFlatItems()

	_, _ = home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}, Alt: true})
	if home.sidebarMode != sidebarFlat {
		t.Fatalf("sidebar mode after Alt+V = %v, want flat", home.sidebarMode)
	}
	for _, item := range home.flatItems {
		if item.Type == session.ItemTypeGroup || item.Type == session.ItemTypeRemoteGroup {
			t.Fatalf("flat sidebar retained group row: %#v", item)
		}
	}
	for _, title := range []string{"a1", "a2", "a3", "b1", "b2"} {
		if sessionIndexByTitle(home, title) < 0 {
			t.Fatalf("flat sidebar dropped session %q", title)
		}
	}
}

func TestSidebarMouseHitTestingTreatsBothSessionLinesAsOneItem(t *testing.T) {
	home, _ := buildTwoGroupHome(t)
	home.embeddedLayout = true
	home.compactSidebar = false
	target := sessionIndexByTitle(home, "a2")
	if target < 0 {
		t.Fatal("missing a2 fixture")
	}
	width := home.sessionsPaneWidth()
	lineBeforeTarget := sidebarItemsHeight(home.flatItems, home.viewOffset, target, width)
	secondLineY := home.getListContentStartY() + lineBeforeTarget + 1
	if got := home.mouseYToItemIndex(secondLineY); got != target {
		t.Fatalf("second row line mapped to item %d, want %d", got, target)
	}
}

func TestEmbeddedSessionRowsFallBackToOneLineInNarrowSidebar(t *testing.T) {
	home, inst, _ := armHomeWithOneSession(t)
	home.embeddedLayout = true
	home.refreshSessionRenderSnapshot([]*session.Instance{inst})
	snapshot := home.getSessionRenderSnapshot()

	var b strings.Builder
	home.renderSessionItem(&b, session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1}, false, snapshot, embeddedCardMinWidth-1)
	lines := strings.Split(strings.TrimSuffix(ansi.Strip(b.String()), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("narrow session row rendered %d lines, want 1:\n%s", len(lines), ansi.Strip(b.String()))
	}
}

func TestSidebarPresentationRoundTripsThroughUIState(t *testing.T) {
	state := uiState{SidebarMode: string(sidebarFlat), SidebarWidthCustomized: true}
	home := NewHome()
	home.embeddedLayout = true
	home.compactSidebar = true
	home.applyUIState(state)
	if home.sidebarMode != sidebarFlat {
		t.Fatalf("restored sidebar mode = %v, want flat", home.sidebarMode)
	}
	if home.compactSidebar {
		t.Fatal("customized sidebar width restored as compact")
	}

	home.applyUIState(uiState{SidebarMode: "invalid"})
	if home.sidebarMode != sidebarGrouped {
		t.Fatalf("invalid persisted sidebar mode = %v, want grouped fallback", home.sidebarMode)
	}
	if !home.compactSidebar {
		t.Fatal("default sidebar state did not restore compact width")
	}
}

func TestEmbeddedSessionRowsUseTwoLines(t *testing.T) {
	home, inst, _ := armHomeWithOneSession(t)
	home.embeddedLayout = true
	home.refreshSessionRenderSnapshot([]*session.Instance{inst})

	var b strings.Builder
	home.renderSessionItem(&b, session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1}, false, home.getSessionRenderSnapshot(), 48)
	lines := strings.Split(strings.TrimSuffix(ansi.Strip(b.String()), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("session row rendered %d lines, want 2:\n%s", len(lines), ansi.Strip(b.String()))
	}
	if !strings.Contains(lines[0], "focused-session") {
		t.Fatalf("primary line missing simplified name: %q", lines[0])
	}
	if !strings.Contains(lines[1], "idle") || !strings.Contains(lines[1], "claude") {
		t.Fatalf("secondary line should contain state and agent: %q", lines[1])
	}
}

func TestCompactSidebarUsesEmbeddedWidth(t *testing.T) {
	home := NewHome()
	home.embeddedLayout = true
	home.compactSidebar = true
	home.width = 200
	left, right := home.splitPaneWidths()
	if left != 36 {
		t.Fatalf("compact sidebar width = %d, want 36 (18%% of 200)", left)
	}
	if left+paneSeparatorWidth+right != home.width {
		t.Fatalf("pane widths do not fill viewport: %d + %d + %d != %d", left, paneSeparatorWidth, right, home.width)
	}
}

func TestClassicStyleRestoresClassicWidthTitleAndRows(t *testing.T) {
	home, inst, _ := armHomeWithOneSession(t)
	home.embeddedLayout = false
	home.compactSidebar = true // remembered Embedded preference must not affect classic mode
	home.width = 200
	home.previewPct = session.DefaultPreviewPct
	home.refreshSessionRenderSnapshot([]*session.Instance{inst})

	left, right := home.splitPaneWidths()
	if left != 70 || left+paneSeparatorWidth+right != home.width {
		t.Fatalf("classic split = (%d, %d), want 70-column session pane filling width %d", left, right, home.width)
	}
	if got := home.sidebarTitle(); got != "SESSIONS" {
		t.Fatalf("classic sidebar title = %q, want SESSIONS", got)
	}

	var b strings.Builder
	home.renderSessionItem(&b, session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1}, false, home.getSessionRenderSnapshot(), left)
	lines := strings.Split(strings.TrimSuffix(ansi.Strip(b.String()), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("classic session row rendered %d lines, want 1:\n%s", len(lines), ansi.Strip(b.String()))
	}
}

func TestClassicStyleIgnoresEmbeddedFlatSidebarToggle(t *testing.T) {
	home, _ := buildTwoGroupHome(t)
	home.storage = nil
	home.embeddedLayout = false
	home.sidebarMode = sidebarGrouped

	_, _ = home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}, Alt: true})
	if home.sidebarMode != sidebarGrouped {
		t.Fatalf("classic Alt+V changed sidebar mode to %v", home.sidebarMode)
	}
}

func TestCompactSidebarKeepsSessionMetadataMinimal(t *testing.T) {
	home, inst, _ := armHomeWithOneSession(t)
	home.embeddedLayout = true
	home.compactSidebar = true
	inst.WorktreeBranch = "wt/very-visible-branch"
	snapshot := map[string]sessionRenderState{
		inst.ID: {
			status:    session.StatusRunning,
			tool:      "codex",
			paneTitle: "long live activity that belongs in the session pane",
		},
	}

	var b strings.Builder
	home.renderSessionItem(&b, session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1}, false, snapshot, 48)
	row := ansi.Strip(b.String())
	for _, want := range []string{"running", "codex"} {
		if !strings.Contains(row, want) {
			t.Fatalf("compact row missing %q: %q", want, row)
		}
	}
	for _, unwanted := range []string{"wt/very-visible-branch", "long live activity"} {
		if strings.Contains(row, unwanted) {
			t.Fatalf("compact row retained noisy metadata %q: %q", unwanted, row)
		}
	}
}

func TestSidebarItemRenderHeightMatchesRowShape(t *testing.T) {
	inst := session.NewInstanceWithTool("two-line", "/tmp/two-line", "claude")
	if got := sidebarItemRenderHeight(session.Item{Type: session.ItemTypeSession, Session: inst}); got != 2 {
		t.Fatalf("session row height = %d, want 2", got)
	}
	if got := sidebarItemRenderHeight(session.Item{Type: session.ItemTypeGroup}); got != 1 {
		t.Fatalf("group row height = %d, want 1", got)
	}
	if got := sidebarItemRenderHeight(session.Item{Type: session.ItemTypeDivider}); got != 1 {
		t.Fatalf("divider row height = %d, want 1", got)
	}
}
