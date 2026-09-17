package ui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestLayoutFollowupPagingMixedRows(t *testing.T) {
	setIsolatedAgentDeckDir(t)
	items := []session.Item{
		{Type: session.ItemTypeGroup},
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "a"}},
		{Type: session.ItemTypeDivider},
		{Type: session.ItemTypeWindow},
		{Type: session.ItemTypeRemoteSession},
		{Type: session.ItemTypeSession, CreatingID: "pending"},
		{Type: session.ItemTypeRemoteGroup},
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "b"}},
	}
	home := newTestHomeWithItems(120, 40, items)
	home.embeddedLayout = true
	home.compactSidebar = false
	home.sidebarDensity = session.SidebarDensityFull
	for _, tc := range []struct {
		cursor int
		lines  int
		dir    int
		want   int
	}{
		{0, 4, 1, 1}, // Row starts are 0, 1, 4(divider), 5, 6, 8, 9, 10.
		{0, 5, 1, 3},
		{0, 7, 1, 4},
		{0, 9, 1, 6},
		{0, 10, 1, 7},
		{7, 4, -1, 3},
		{7, 5, -1, 4},
		{7, 6, -1, 4}, // Do not commit the divider on upward paging either.
		{7, 9, -1, 6},
		{1, 1, 1, 2}, // First selectable step may cross a divider and exceed budget.
		{3, 1, -1, 2},
		{0, 1, 1, 1}, // First card itself can exceed the budget.
	} {
		t.Run(fmt.Sprintf("%d/%d/%d", tc.cursor, tc.lines, tc.dir), func(t *testing.T) {
			home.cursor = tc.cursor
			if got := home.pageStepItems(tc.lines, tc.dir); got != tc.want {
				t.Fatalf("page step = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestLayoutFollowupPagingChargesRowStartDistance(t *testing.T) {
	setIsolatedAgentDeckDir(t)
	home := newTestHomeWithItems(120, 40, []session.Item{
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "local"}},
		{Type: session.ItemTypeGroup},
		{Type: session.ItemTypeRemoteSession},
	})
	home.embeddedLayout = true
	home.compactSidebar = false
	home.sidebarDensity = session.SidebarDensityFull
	// Rendered row starts are 0, 3 and 4. Downward movement charges
	// the row being left; upward movement charges the row being entered.
	if got := home.pageStepItems(3, 1); got != 1 {
		t.Fatalf("three lines down from local = %d rows, want group only", got)
	}
	home.cursor = 2
	if got := home.pageStepItems(3, -1); got != 1 {
		t.Fatalf("three lines up from remote = %d rows, want group only", got)
	}
	home.embeddedLayout = false
	if got := home.pageStepItems(3, 1); got != 3 {
		t.Fatalf("classic page arithmetic changed: got %d, want 3", got)
	}
}

func TestLayoutFollowupPageKeysStopBeforeDividerOverflow(t *testing.T) {
	setIsolatedAgentDeckDir(t)
	for _, tc := range []struct {
		name   string
		key    tea.KeyType
		height int
		cursor int
		want   int
		half   bool
	}{
		{"full down", tea.KeyCtrlF, 11, 0, 1, false},
		{"full up", tea.KeyCtrlB, 11, 4, 3, false},
		{"half down", tea.KeyCtrlD, 15, 0, 1, true},
		{"half up", tea.KeyCtrlU, 15, 4, 3, true},
		{"pgdown", tea.KeyPgDown, 15, 0, 1, true},
		{"pgup", tea.KeyPgUp, 15, 4, 3, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := newTestHomeWithItems(120, tc.height, []session.Item{
				{Type: session.ItemTypeGroup},
				{Type: session.ItemTypeSession, Session: &session.Instance{ID: "a"}},
				{Type: session.ItemTypeDivider},
				{Type: session.ItemTypeSession, Session: &session.Instance{ID: "b"}},
				{Type: session.ItemTypeGroup},
			})
			home.storage = nil
			home.embeddedLayout = true
			home.compactSidebar = false
			home.sidebarDensity = session.SidebarDensityFull
			home.debugMode = false
			home.updateInfo = nil
			home.maintenanceMsg = ""
			home.cursor = tc.cursor
			budget := home.getVisibleHeight()
			if tc.half {
				budget /= 2
			}
			if budget != 4 {
				t.Fatalf("fixture page budget = %d, want 4", budget)
			}
			_, _ = home.handleMainKey(tea.KeyMsg{Type: tc.key})
			if home.cursor != tc.want {
				t.Fatalf("cursor = %d, want %d", home.cursor, tc.want)
			}
		})
	}
}

func TestLayoutFollowupFlatRemotesIgnoreButPreserveGroupedFolds(t *testing.T) {
	setIsolatedAgentDeckDir(t)
	for _, fold := range []string{"remotes/dev", "remotes/dev/work"} {
		t.Run(fold, func(t *testing.T) {
			home := NewHome()
			home.storage = nil
			home.width, home.height = 120, 40
			home.embeddedLayout = true
			home.sidebarMode = sidebarGrouped
			home.groupTree = session.NewGroupTree(nil)
			home.remoteGroupsCollapsed = map[string]bool{fold: true}
			home.remoteSessions = map[string][]session.RemoteSessionInfo{
				"dev": {
					{ID: "a1", Group: "work"},
					{ID: "a2", Group: "work"},
					{ID: "b", Group: "work/api"},
				},
				"other": {{ID: "o", Group: "work"}},
			}
			home.remoteSessionOrder = remoteOrder{"dev": {"work": {"a2", "a1"}}}
			remoteIDs := func() []string {
				var ids []string
				for _, item := range home.flatItems {
					if item.Type == session.ItemTypeRemoteSession {
						ids = append(ids, item.RemoteName+"/"+item.RemoteSession.ID)
					}
				}
				return ids
			}
			home.rebuildFlatItems()
			grouped := remoteIDs()
			if !reflect.DeepEqual(grouped, []string{"other/o"}) {
				t.Fatalf("grouped fixture = %v, want only other/o", grouped)
			}
			toggle := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}, Alt: true}
			_, _ = home.handleMainKey(toggle)
			if home.sidebarMode != sidebarFlat {
				t.Fatal("Alt+V did not enter flat presentation")
			}
			if got, want := remoteIDs(), []string{"dev/a2", "dev/a1", "dev/b", "other/o"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("flat remote IDs = %v, want %v", got, want)
			}
			for _, item := range home.flatItems {
				if item.Type == session.ItemTypeGroup || item.Type == session.ItemTypeRemoteGroup {
					t.Fatalf("flat presentation retained a group: %+v", item)
				}
			}
			_, _ = home.handleMainKey(toggle)
			if got := remoteIDs(); !reflect.DeepEqual(got, grouped) {
				t.Fatalf("grouped round trip = %v, want %v", got, grouped)
			}
			if !reflect.DeepEqual(home.remoteGroupsCollapsed, map[string]bool{fold: true}) {
				t.Fatalf("flat mode mutated saved folds: %v", home.remoteGroupsCollapsed)
			}
		})
	}
}

func TestLayoutFollowupRemoteCardStatusParity(t *testing.T) {
	setIsolatedAgentDeckDir(t)
	home := NewHome()
	home.embeddedLayout = true
	home.width, home.height = 120, 40
	for _, tc := range []struct {
		name     string
		status   string
		substate string
		archived bool
		glyph    string
	}{
		{"archived running", "running", "", true, "■"},
		{"stopped", "stopped", "", false, "■"},
		{"model unavailable", "error", string(session.SubstateModelUnavailable), false, "⚡"},
		{"auth error", "error", string(session.SubstateAuth401), false, "🔒"},
		{"auth stopped", "stopped", string(session.SubstateAuth401), false, "🔒"},
	} {
		for _, density := range []string{session.SidebarDensityMinimal, session.SidebarDensityCompact, session.SidebarDensityFull} {
			for _, selected := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/selected=%t", tc.name, density, selected), func(t *testing.T) {
					home.sidebarDensity = density
					item := session.Item{
						Type:       session.ItemTypeRemoteSession,
						RemoteName: "dev",
						RemoteSession: &session.RemoteSessionInfo{
							ID: "r", Title: "remote", Status: tc.status,
							Substate: tc.substate, Archived: tc.archived,
						},
					}
					var b strings.Builder
					home.renderRemoteSessionItemAtWidth(&b, item, selected, 48)
					rendered := ansi.Strip(b.String())
					if !strings.Contains(rendered, tc.glyph) {
						t.Fatalf("card missing %q: %s", tc.glyph, rendered)
					}
					if tc.archived && density != session.SidebarDensityMinimal {
						if !strings.Contains(rendered, "archived") || strings.Contains(rendered, "running") {
							t.Fatalf("archived metadata is misleading: %s", rendered)
						}
					}
				})
			}
		}
	}
}

func TestLayoutFollowupHelpUsesRunningLayoutUntilRestart(t *testing.T) {
	for _, running := range []bool{false, true} {
		t.Run(fmt.Sprintf("running=%t", running), func(t *testing.T) {
			setIsolatedAgentDeckDir(t)
			if err := session.SaveUserConfig(&session.UserConfig{UI: session.UISettings{EmbeddedTerminal: &running}}); err != nil {
				t.Fatal(err)
			}
			session.ClearUserConfigCache()
			home := NewHome()
			home.width, home.height = 140, 160
			next := !running
			ui := session.UISettings{EmbeddedTerminal: &next}
			if err := session.SaveUserConfig(&session.UserConfig{UI: ui}); err != nil {
				t.Fatal(err)
			}
			session.ClearUserConfigCache()
			home.applySavedLayoutSettings(ui)
			if home.embeddedLayout != running {
				t.Fatal("saving the preference changed the active layout")
			}
			assertHelp := func(h *Home, embedded bool) {
				t.Helper()
				_, _ = h.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
				rendered := ansi.Strip(h.helpOverlay.View())
				hasEmbeddedEnter := strings.Contains(rendered, "Focus embedded terminal")
				hasFlatShortcut := strings.Contains(rendered, "Alt+V")
				if hasEmbeddedEnter != embedded || hasFlatShortcut != embedded {
					t.Fatalf("help does not match running embedded=%t:\n%s", embedded, rendered)
				}
				if !embedded && !strings.Contains(rendered, "Attach to selected session") {
					t.Fatalf("classic Enter help missing:\n%s", rendered)
				}
			}
			assertHelp(home, running)
			restarted := NewHome()
			restarted.width, restarted.height = 140, 160
			if restarted.embeddedLayout != next {
				t.Fatal("new Home did not load the saved layout")
			}
			assertHelp(restarted, next)
		})
	}
}
