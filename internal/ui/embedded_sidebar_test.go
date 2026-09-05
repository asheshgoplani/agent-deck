package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

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
	lineBeforeTarget := home.sidebarItemsHeight(home.flatItems, home.viewOffset, target, width)
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
	lines := sidebarDensityRowLines(session.DefaultSidebarDensity)
	if got := sidebarItemRenderHeightDensity(session.Item{Type: session.ItemTypeSession, Session: inst}, lines); got != 2 {
		t.Fatalf("session row height = %d, want 2", got)
	}
	if got := sidebarItemRenderHeightDensity(session.Item{Type: session.ItemTypeGroup}, lines); got != 1 {
		t.Fatalf("group row height = %d, want 1", got)
	}
	if got := sidebarItemRenderHeightDensity(session.Item{Type: session.ItemTypeDivider}, lines); got != 1 {
		t.Fatalf("divider row height = %d, want 1", got)
	}
	// Remote rows never grow past one metadata line.
	if got := sidebarItemRenderHeightDensity(session.Item{Type: session.ItemTypeRemoteSession}, 3); got != 2 {
		t.Fatalf("remote row height at full density = %d, want 2", got)
	}
	if got := sidebarItemRenderHeightDensity(session.Item{Type: session.ItemTypeRemoteSession}, 1); got != 1 {
		t.Fatalf("remote row height at minimal density = %d, want 1", got)
	}
}

// The same session must render as a three-, two-, or one-line card depending
// on [ui].sidebar_density, and every measurement the sidebar makes has to
// agree with what the renderer emitted.
func TestSidebarDensityRendersConfiguredLineCount(t *testing.T) {
	cases := []struct {
		density string
		want    int
	}{
		{session.SidebarDensityFull, 3},
		{session.SidebarDensityCompact, 2},
		{session.SidebarDensityMinimal, 1},
		{"", 2},
		{"nonsense", 2},
	}
	for _, tc := range cases {
		t.Run(tc.density, func(t *testing.T) {
			home, inst, _ := armHomeWithOneSession(t)
			home.embeddedLayout = true
			home.sidebarDensity = session.UISettings{SidebarDensity: tc.density}.GetSidebarDensity()
			home.refreshSessionRenderSnapshot([]*session.Instance{inst})

			item := session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1}
			var b strings.Builder
			home.renderSessionItem(&b, item, false, home.getSessionRenderSnapshot(), 48)
			rendered := ansi.Strip(b.String())
			lines := strings.Split(strings.TrimSuffix(rendered, "\n"), "\n")
			if len(lines) != tc.want {
				t.Fatalf("density %q rendered %d lines, want %d:\n%s", tc.density, len(lines), tc.want, rendered)
			}
			if got := home.sidebarItemRenderHeightAtWidth(item, 48); got != tc.want {
				t.Fatalf("density %q measured height %d, want %d", tc.density, got, tc.want)
			}
			if !strings.Contains(lines[0], "focused-session") {
				t.Fatalf("density %q lost the session name: %q", tc.density, lines[0])
			}
			// The agent has to stay identifiable at every density: the tool
			// name on a metadata line, or its marker inline at one line.
			if tc.want == 1 {
				if !strings.Contains(lines[0], sidebarToolIcon("claude")) {
					t.Fatalf("one-line density must carry the tool marker inline: %q", lines[0])
				}
			} else {
				if !strings.Contains(rendered, "claude") {
					t.Fatalf("density %q dropped the tool name:\n%s", tc.density, rendered)
				}
				// The card closes with the elbow on its last metadata line.
				if !strings.Contains(lines[len(lines)-1], "╰") {
					t.Fatalf("density %q last line missing the closing elbow: %q", tc.density, lines[len(lines)-1])
				}
			}
		})
	}
}

// The rail ships two-line by default; pin that so the default is a choice.
func TestSidebarDensityDefaultsToCompact(t *testing.T) {
	if session.DefaultSidebarDensity != session.SidebarDensityCompact {
		t.Fatalf("default sidebar density = %q, want %q", session.DefaultSidebarDensity, session.SidebarDensityCompact)
	}
	home := NewHome()
	if home.sidebarDensity != session.SidebarDensityCompact {
		t.Fatalf("fresh Home sidebar density = %q, want compact", home.sidebarDensity)
	}
	if got := (session.UISettings{}).GetSidebarDensity(); got != session.SidebarDensityCompact {
		t.Fatalf("unset [ui].sidebar_density = %q, want compact", got)
	}
}

// Density is an embedded-layout knob: classic rows stay one line regardless.
func TestSidebarDensityDoesNotAffectClassicLayout(t *testing.T) {
	home, inst, _ := armHomeWithOneSession(t)
	home.embeddedLayout = false
	home.sidebarDensity = session.SidebarDensityFull
	home.refreshSessionRenderSnapshot([]*session.Instance{inst})

	item := session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1}
	var b strings.Builder
	home.renderSessionItem(&b, item, false, home.getSessionRenderSnapshot(), 70)
	lines := strings.Split(strings.TrimSuffix(ansi.Strip(b.String()), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("classic row rendered %d lines under full density, want 1", len(lines))
	}
	if got := home.sidebarItemRenderHeightAtWidth(item, 70); got != 1 {
		t.Fatalf("classic row measured %d lines under full density, want 1", got)
	}
}

// "auto" is not a fourth card shape — it is a rule for picking one of the
// three. It spends the most height per session that still leaves the whole
// visible list on screen, and it reads only the list and the available height,
// so it resolves the same way for the renderer, the scroll window, and the
// click hit-test.
func TestSidebarAutoDensityPicksWidestFittingCard(t *testing.T) {
	sessions := func(n int) []session.Item {
		items := make([]session.Item, 0, n)
		for i := range n {
			items = append(items, session.Item{
				Type:    session.ItemTypeSession,
				Session: &session.Instance{ID: fmt.Sprintf("s%d", i), Title: fmt.Sprintf("s%d", i)},
			})
		}
		return items
	}
	cases := []struct {
		name   string
		count  int
		budget int
		want   int
	}{
		{"room to spare", 3, 30, 3},
		{"exactly three lines each", 5, 15, 3},
		{"one line short of full", 5, 14, 2},
		{"exactly two lines each", 6, 12, 2},
		{"one line short of compact", 6, 11, 1},
		{"more sessions than lines", 40, 10, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sidebarAutoRowLinesFor(sessions(tc.count), 60, tc.budget)
			if got != tc.want {
				t.Fatalf("auto density for %d sessions in %d lines = %d, want %d", tc.count, tc.budget, got, tc.want)
			}
		})
	}
}

// The height auto is fitting to is the live sidebar budget, so shrinking the
// terminal tightens the rail without anyone touching the setting.
func TestSidebarAutoDensityTightensAsTerminalShrinks(t *testing.T) {
	cases := []struct {
		height int
		want   int
	}{
		{40, 3}, // 5 sessions x 3 lines + 2 group rows = 17, well inside the budget
		{23, 2}, // budget 16: 17 no longer fits, 12 does
		{17, 1}, // budget 10: even 12 is too tall
	}
	for _, tc := range cases {
		home, _ := buildTwoGroupHome(t)
		home.embeddedLayout = true
		home.compactSidebar = false // a rail this narrow collapses to one line anyway
		home.sidebarDensity = session.SidebarDensityAuto
		home.height = tc.height
		if got := home.sidebarRowLines(); got != tc.want {
			budget, width := home.sidebarLineBudget()
			t.Fatalf("auto density at height %d = %d lines, want %d (budget %d, width %d)", tc.height, got, tc.want, budget, width)
		}
	}
}

// The list auto measures is h.flatItems, which already omits the sessions
// inside a collapsed group — so closing a group is what gives the metadata
// lines back, and opening one is what takes them away.
func TestSidebarAutoDensityFollowsOpenGroups(t *testing.T) {
	home, _ := buildTwoGroupHome(t)
	home.embeddedLayout = true
	home.compactSidebar = false // a rail this narrow collapses to one line anyway
	home.sidebarDensity = session.SidebarDensityAuto
	home.height = 23

	open := home.sidebarRowLines()
	if open != 2 {
		t.Fatalf("auto density with both groups open = %d, want 2", open)
	}

	home.groupTree.CollapseGroup("alpha")
	home.groupTree.CollapseGroup("beta")
	home.rebuildFlatItems()
	if got := home.sidebarRowLines(); got != 3 {
		t.Fatalf("auto density with groups collapsed = %d, want 3", got)
	}

	home.groupTree.ExpandGroup("alpha")
	home.groupTree.ExpandGroup("beta")
	home.rebuildFlatItems()
	if got := home.sidebarRowLines(); got != open {
		t.Fatalf("auto density after reopening groups = %d, want %d", got, open)
	}
}

// Whatever auto resolves to, the renderer has to emit exactly that many lines —
// the same contract the fixed densities hold.
func TestSidebarAutoDensityRendersWhatItMeasures(t *testing.T) {
	seen := map[int]bool{}
	for _, height := range []int{40, 26, 18} {
		home, inst, _ := armHomeWithOneSession(t)
		home.embeddedLayout = true
		home.compactSidebar = false // a rail this narrow collapses to one line anyway
		home.sidebarDensity = session.SidebarDensityAuto
		home.height = height
		home.flatItems = make([]session.Item, 0, 12)
		for i := range 12 {
			home.flatItems = append(home.flatItems, session.Item{
				Type:    session.ItemTypeSession,
				Session: &session.Instance{ID: fmt.Sprintf("filler%d", i), Title: fmt.Sprintf("filler%d", i)},
			})
		}
		home.refreshSessionRenderSnapshot([]*session.Instance{inst})

		item := session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1}
		var b strings.Builder
		home.renderSessionItem(&b, item, false, home.getSessionRenderSnapshot(), 48)
		rendered := ansi.Strip(b.String())
		lines := strings.Split(strings.TrimSuffix(rendered, "\n"), "\n")
		got := home.sidebarItemRenderHeightAtWidth(item, 48)
		if got != len(lines) {
			t.Fatalf("height %d: auto measured %d lines but rendered %d:\n%s", height, got, len(lines), rendered)
		}
		seen[got] = true
	}
	if len(seen) < 2 {
		t.Fatalf("auto never changed density across the sampled heights: %v", seen)
	}
}

// An unsized Home has no budget to fit anything to, so auto must not silently
// resolve to the tallest card before the first WindowSizeMsg arrives.
func TestSidebarAutoDensityBeforeLayoutUsesDefault(t *testing.T) {
	home := NewHome()
	home.embeddedLayout = true
	home.sidebarDensity = session.SidebarDensityAuto
	home.height = 0
	if got, want := home.sidebarRowLines(), sidebarDensityRowLines(session.DefaultSidebarDensity); got != want {
		t.Fatalf("unsized auto density = %d, want the default %d", got, want)
	}
}

func TestSidebarDensityAutoIsAConfigurableValue(t *testing.T) {
	for _, raw := range []string{"auto", "AUTO", "  Auto  "} {
		if got := (session.UISettings{SidebarDensity: raw}).GetSidebarDensity(); got != session.SidebarDensityAuto {
			t.Fatalf("[ui].sidebar_density = %q resolved to %q, want auto", raw, got)
		}
	}
	found := false
	for i, val := range sidebarDensityValues {
		if val == session.SidebarDensityAuto {
			found = true
			if sidebarDensityNames[i] != "Auto" {
				t.Fatalf("auto radio label = %q, want Auto", sidebarDensityNames[i])
			}
		}
	}
	if !found {
		t.Fatal("Settings > Interface cannot select auto: missing from sidebarDensityValues")
	}
	if len(sidebarDensityNames) != len(sidebarDensityValues) {
		t.Fatalf("density radio names/values out of step: %d vs %d", len(sidebarDensityNames), len(sidebarDensityValues))
	}
}

func TestSidebarDensityNarrowWidthStillCollapsesToOneLine(t *testing.T) {
	home, inst, _ := armHomeWithOneSession(t)
	home.embeddedLayout = true
	home.sidebarDensity = session.SidebarDensityFull
	home.refreshSessionRenderSnapshot([]*session.Instance{inst})

	item := session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1}
	if got := home.sidebarItemRenderHeightAtWidth(item, embeddedCardMinWidth-1); got != 1 {
		t.Fatalf("narrow full-density row height = %d, want 1", got)
	}
}

// A restarting session spins on its identity line and says so on the
// metadata line, the same way the classic row does.
func TestEmbeddedSessionRowShowsRestartProgress(t *testing.T) {
	home, inst, _ := armHomeWithOneSession(t)
	home.embeddedLayout = true
	home.resumingSessions[inst.ID] = time.Now()
	home.refreshSessionRenderSnapshot([]*session.Instance{inst})

	var b strings.Builder
	home.renderSessionItem(&b, session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1}, false, home.getSessionRenderSnapshot(), 48)
	if row := ansi.Strip(b.String()); !strings.Contains(row, "restarting…") {
		t.Fatalf("restarting row did not say so: %q", row)
	}
}
