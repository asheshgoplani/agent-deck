package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/feedback"
	"github.com/asheshgoplani/agent-deck/internal/recall/query"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// dialogFitCase renders one dialog state at one terminal size. mustShow is
// text that has to be on screen (the dialog's title or its focused row).
type dialogFitCase struct {
	name     string
	w, h     int
	mustShow []string
	view     func() string
}

// dialogFitSizes are the visual check widths plus the smallest supported
// terminal, 60x15.
var dialogFitSizes = [][2]int{{60, 15}, {80, 24}, {120, 40}, {200, 50}}

// dialogFitShortSizes are the sizes where a dialog scrolls: the focus-last
// states are pinned there.
var dialogFitShortSizes = [][2]int{{60, 15}, {80, 24}}

func dialogFitSearchItems() []*session.Instance {
	var items []*session.Instance
	for i := 1; i <= 11; i++ {
		items = append(items, &session.Instance{ID: fmt.Sprintf("s%d", i), Title: fmt.Sprintf("session-%02d", i), Tool: "claude"})
	}
	return items
}

// dialogFitCases are the round 1 "nothing clips" dialogs. The frames are
// pinned as goldens (UPDATE_GOLDEN=1 rewrites) so a dialog that already fit
// at 120x40 and 200x50 is proven to render exactly as before.
func dialogFitCases() []dialogFitCase {
	var cases []dialogFitCase
	for _, sz := range dialogFitSizes {
		w, h := sz[0], sz[1]
		cases = append(cases,
			dialogFitCase{name: "edit", w: w, h: h, mustShow: []string{"Edit Session", "Esc cancel"}, view: func() string {
				d := NewEditSessionDialog()
				d.SetSize(w, h)
				d.Show(sampleInstance())
				return d.View()
			}},
			dialogFitCase{name: "search", w: w, h: h, mustShow: []string{"Local Search", "session-01", "[Esc] Cancel"}, view: func() string {
				s := NewSearch()
				s.SetSize(w, h)
				s.SetItems(dialogFitSearchItems())
				s.Show()
				return s.View()
			}},
			dialogFitCase{name: "search-recall-off", w: w, h: h, mustShow: []string{"Local Search", "Recall is off", "[Esc] Cancel"}, view: func() string {
				s := NewSearch()
				s.SetSize(w, h)
				s.SetItems(dialogFitSearchItems())
				s.Show()
				s.SetNotice(recallOffNotice)
				return s.View()
			}},
			dialogFitCase{name: "wizard-tool", w: w, h: h, mustShow: []string{"[Tool]", "Select Default AI Tool", "Esc: back"}, view: func() string {
				wz := NewSetupWizard()
				wz.SetSize(w, h)
				wz.Show()
				wz.currentStep = stepToolSelection
				return wz.View()
			}},
			dialogFitCase{name: "group-create", w: w, h: h, mustShow: []string{"Create Subgroup", "Default Path:", "Esc cancel"}, view: func() string {
				g := NewGroupDialog()
				g.SetSize(w, h)
				g.ShowCreateWithContext("alpha", "alpha")
				return g.View()
			}},
			dialogFitCase{name: "feedback", w: w, h: h, mustShow: []string{"How's agent-deck v1.16.16?", "[Esc] Ask later"}, view: func() string {
				d := NewFeedbackDialog()
				d.SetSize(w, h)
				d.Show("1.16.16", &feedback.State{MaxShows: 3, FeedbackEnabled: true}, feedback.NewSender())
				return d.View()
			}},
		)
	}
	// Focus on the last row (or a later step) on short terminals: the
	// scrolled state keeps the title, the focused row and the key hints on
	// screen.
	for _, sz := range dialogFitShortSizes {
		w, h := sz[0], sz[1]
		cases = append(cases,
			dialogFitCase{name: "fork-picker-first", w: w, h: h, mustShow: []string{"Fork Session", "▶ main", "Enter select", "Esc close"}, view: func() string {
				return dialogFitForkPicker(w, h, 0).View()
			}},
			dialogFitCase{name: "fork-picker-last", w: w, h: h, mustShow: []string{"Fork Session", "▶ feature/two", "Enter select", "Esc close"}, view: func() string {
				return dialogFitForkPicker(w, h, 2).View()
			}},
			dialogFitCase{name: "edit-last-field", w: w, h: h, mustShow: []string{"Edit Session", "Esc cancel"}, view: func() string {
				d := NewEditSessionDialog()
				d.SetSize(w, h)
				d.Show(sampleInstance())
				d.focusIndex = len(d.fields) - 1
				return d.View()
			}},
			dialogFitCase{name: "search-cursor-last", w: w, h: h, mustShow: []string{"Local Search", "› session-11", "[Esc] Cancel"}, view: func() string {
				s := NewSearch()
				s.SetSize(w, h)
				s.SetItems(dialogFitSearchItems())
				s.Show()
				s.SetNotice(recallOffNotice)
				s.View()
				s.cursor = len(s.results) - 1 // the eleventh result; the ten-row window follows it
				return s.View()
			}},
			dialogFitCase{name: "wizard-tool-last", w: w, h: h, mustShow: []string{"[Tool]", "omp", "Esc: back"}, view: func() string {
				wz := NewSetupWizard()
				wz.SetSize(w, h)
				wz.Show()
				wz.currentStep = stepToolSelection
				wz.selectedTool = len(wz.toolOptions) - 1
				return wz.View()
			}},
			dialogFitCase{name: "wizard-claude-last", w: w, h: h, mustShow: []string{"[Claude]", "Claude config directory:", "Esc: back"}, view: func() string {
				wz := NewSetupWizard()
				wz.SetSize(w, h)
				wz.Show()
				wz.currentStep = stepClaudeSettings
				wz.claudeSettingsCursor = 2
				return wz.View()
			}},
			dialogFitCase{name: "wizard-ready", w: w, h: h, mustShow: []string{"[Ready]", "Esc: back"}, view: func() string {
				wz := NewSetupWizard()
				wz.SetSize(w, h)
				wz.Show()
				wz.currentStep = stepReady
				return wz.View()
			}},
			dialogFitCase{name: "fork-options", w: w, h: h, mustShow: []string{"Fork Session", "Skip permissions", "Tab next", "Esc cancel"}, view: func() string {
				d := dialogFitFork(w, h, false)
				d.focusIndex = len(d.focusTargets()) - 1
				d.updateFocus()
				return d.View()
			}},
			dialogFitCase{name: "skills-last", w: w, h: h, mustShow: []string{"Skills Manager", "skill-19", "Esc cancel"}, view: func() string {
				d := dialogFitSkills(w, h)
				d.availableIdx = len(d.available) - 1
				return d.View()
			}},
			dialogFitCase{name: "recall-last", w: w, h: h, mustShow: []string{"Recall", "› recall-hit-14", "[Tab] Local", "[Esc] Cancel"}, view: func() string {
				gs := dialogFitRecall(w, h)
				gs.cursor = len(gs.results) - 1
				return gs.View()
			}},
			dialogFitCase{name: "mcp-list-last", w: w, h: h, mustShow: []string{"MCP Manager", "mcp-24", "Tab scope", "Esc cancel"}, view: func() string {
				m := dialogFitMCP(w, h)
				m.localAvailableIdx = len(m.localAvailable) - 1
				return m.View()
			}},
		)
	}
	// The dialogs whose footers or windowing the round touched, at every size.
	for _, sz := range dialogFitSizes {
		w, h := sz[0], sz[1]
		cases = append(cases,
			dialogFitCase{name: "fork", w: w, h: h, mustShow: []string{"Fork Session", "▶ Name:", "Enter create", "Esc cancel", "Tab next", "s sandbox", "Space toggle"}, view: func() string {
				return dialogFitFork(w, h, false).View()
			}},
			dialogFitCase{name: "fork-branch", w: w, h: h, mustShow: []string{"Fork Session", "▶ Branch:", "^F branch search", "Enter create", "Esc cancel", "Tab next"}, view: func() string {
				d := dialogFitFork(w, h, true)
				d.focusIndex = 2 // name, group, branch
				d.updateFocus()
				return d.View()
			}},
			dialogFitCase{name: "skills", w: w, h: h, mustShow: []string{"Skills Manager", "skill-00", "Space move", "Esc cancel"}, view: func() string {
				return dialogFitSkills(w, h).View()
			}},
			dialogFitCase{name: "recall", w: w, h: h, mustShow: []string{"Recall", "recall-hit-00", "[Tab] Local", "[Esc] Cancel"}, view: func() string {
				return dialogFitRecall(w, h).View()
			}},
			dialogFitCase{name: "mcp-list", w: w, h: h, mustShow: []string{"MCP Manager", "mcp-00", "Tab scope", "Space move", "Esc cancel"}, view: func() string {
				return dialogFitMCP(w, h).View()
			}},
		)
	}
	return cases
}

// dialogFitFork is the Fork dialog of a plain session, with the worktree
// branch field shown when worktree is set.
func dialogFitFork(w, h int, worktree bool) *ForkDialog {
	d := NewForkDialog()
	d.SetSize(w, h)
	d.Show("probe", "/nonexistent/probe-project", "alpha", nil, "")
	d.worktreeCapable = worktree
	d.worktreeEnabled = worktree
	return d
}

func dialogFitForkPicker(w, h, cursor int) *ForkDialog {
	d := dialogFitFork(w, h, true)
	d.focusIndex = 2 // name, group, branch
	d.updateFocus()
	d.branchPicker.visible = true
	d.branchPicker.branches = []string{"main", "feature/one", "feature/two"}
	d.branchPicker.cursor = cursor
	d.branchPicker.offset = 0
	return d
}

// dialogFitSkills is the Skills Manager with 20 pool skills available.
func dialogFitSkills(w, h int) *SkillDialog {
	d := NewSkillDialog()
	d.SetSize(w, h)
	d.visible = true
	d.tool = "claude"
	for i := 0; i < 20; i++ {
		d.available = append(d.available, SkillDialogItem{Candidate: session.SkillCandidate{Name: fmt.Sprintf("skill-%02d", i), Source: "pool"}})
	}
	d.column = SkillColumnAvailable
	return d
}

// dialogFitRecall is Recall with an index and 15 hits.
func dialogFitRecall(w, h int) *GlobalSearch {
	var hits []query.Hit
	for i := 0; i < 15; i++ {
		hits = append(hits, query.Hit{SessID: int64(100 + i), Harness: "claude", NativeID: fmt.Sprintf("aaaaaaaa-0000-4000-8000-%012d", i),
			Title: fmt.Sprintf("recall-hit-%02d", i), CWD: "/Users/x/proj", BodyHits: 1}) // no time: the frame is stable
	}
	gs := NewGlobalSearch()
	gs.SetSource(newStubRecall())
	gs.SetSize(w, h)
	gs.Show()
	gs.applySearchResults(query.SearchResult{Hits: hits})
	return gs
}

// dialogFitMCP is the Claude MCP Manager with 25 MCPs available locally.
// Its two columns are wider than the box below 80 columns, so a name can
// wrap there ("probe-" / "mcp-00"); the frames check the name's tail.
func dialogFitMCP(w, h int) *MCPDialog {
	m := NewMCPDialog()
	m.SetSize(w, h)
	m.visible = true
	m.tool = "claude"
	m.scope = MCPScopeLocal
	m.column = MCPColumnAvailable
	for i := 0; i < 25; i++ {
		m.localAvailable = append(m.localAvailable, MCPItem{Name: fmt.Sprintf("probe-mcp-%02d", i), Transport: "stdio"})
	}
	return m
}

func TestDialogFitFramesGolden(t *testing.T) {
	isolateDialogFitHome(t)
	for _, tc := range dialogFitCases() {
		t.Run(fmt.Sprintf("%s_%dx%d", tc.name, tc.w, tc.h), func(t *testing.T) {
			assertFrameGolden(t, fmt.Sprintf("dialog_fit_%s_%dx%d.golden", tc.name, tc.w, tc.h), stripAnsi(tc.view()))
		})
	}
}

// assertDialogFits checks that frame fits a w x h terminal and shows every
// mustShow string.
func assertDialogFits(t *testing.T, frame string, w, h int, mustShow ...string) {
	t.Helper()
	plain := strings.ReplaceAll(stripAnsi(frame), nbsp, " ")
	// Bubble Tea splits the view on "\n" and keeps only the last h lines, so
	// a trailing newline is a row too: a dialog that fills the screen and
	// ends with "\n" loses its top border.
	if lines := strings.Count(plain, "\n") + 1; lines > h {
		t.Errorf("frame is %d lines (trailing newline included) on a %d-row terminal:\n%s", lines, h, plain)
	}
	rows := strings.Split(strings.TrimRight(plain, "\n"), "\n")
	for i, row := range rows {
		if cw := cellWidth(row); cw > w {
			t.Errorf("row %d is %d cells on a %d-column terminal: %q", i, cw, w, row)
		}
	}
	for _, want := range mustShow {
		if !strings.Contains(plain, want) {
			t.Errorf("frame must show %q:\n%s", want, plain)
		}
	}
	// The box's top and bottom border are both on screen.
	if !strings.Contains(plain, "╭") || !strings.Contains(plain, "╰") {
		t.Errorf("dialog border is clipped:\n%s", plain)
	}
}

// isolateDialogFitHome gives the dialogs an empty HOME, so config-driven
// defaults (fork options, hotkeys) are the built-in ones on every machine.
func isolateDialogFitHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
}

func TestDialogFitFramesFitScreen(t *testing.T) {
	isolateDialogFitHome(t)
	for _, tc := range dialogFitCases() {
		t.Run(fmt.Sprintf("%s_%dx%d", tc.name, tc.w, tc.h), func(t *testing.T) {
			assertDialogFits(t, tc.view(), tc.w, tc.h, tc.mustShow...)
		})
	}
}

func TestSettingsPanelFitsScreen(t *testing.T) {
	for _, sz := range dialogFitSizes {
		s := NewSettingsPanel()
		s.SetSize(sz[0], sz[1])
		s.Show()
		assertDialogFits(t, s.View(), sz[0], sz[1], "Settings", "THEME", "j/k Navigate", "Esc Close")
		// The last setting scrolls into view with the box still whole and
		// the help bar still pinned.
		s.cursor = int(settingsCount) - 1
		assertDialogFits(t, s.View(), sz[0], sz[1], "Sidebar density", "j/k Navigate", "Esc Close")
	}
}

func TestMCPDialogFitsScreenAndNamesItsKey(t *testing.T) {
	for _, sz := range dialogFitSizes {
		m := NewMCPDialog()
		m.SetSize(sz[0], sz[1])
		_ = m.Show(t.TempDir(), "sess-1", "claude")
		m.visible = true
		view := m.View()
		mustShow := []string{"MCP Manager", "Tab scope", "Esc cancel"}
		if sz[1] >= 24 {
			// At 60x15 the help text scrolls; its top stays on screen.
			mustShow = append(mustShow, "Then press m again")
		}
		assertDialogFits(t, view, sz[0], sz[1], mustShow...)
		if strings.Contains(stripAnsi(view), "press M again") {
			t.Errorf("M is Move to group; the MCP Manager key is m:\n%s", stripAnsi(view))
		}
		for _, row := range strings.Split(stripAnsi(view), "\n") {
			if strings.HasPrefix(strings.Trim(row, " │"), "│ Esc") {
				t.Errorf("footer wrapped onto a second row:\n%s", stripAnsi(view))
			}
		}
	}
}

func TestRecallFitsScreenAndFollowsCursor(t *testing.T) {
	var hits []query.Hit
	for i := 0; i < 15; i++ {
		hits = append(hits, query.Hit{SessID: int64(100 + i), Harness: "claude", NativeID: fmt.Sprintf("aaaaaaaa-0000-4000-8000-%012d", i),
			Title: fmt.Sprintf("recall-hit-%02d", i), CWD: "/Users/x/proj", EndedAt: time.Now().Add(-time.Hour).Unix(), BodyHits: 1})
	}
	for _, sz := range dialogFitSizes {
		gs := NewGlobalSearch()
		gs.SetSource(newStubRecall())
		gs.SetSize(sz[0], sz[1])
		gs.Show()
		gs.applySearchResults(query.SearchResult{Hits: hits})
		assertDialogFits(t, gs.View(), sz[0], sz[1], "Recall", "recall-hit-00", "[Esc] Cancel")
		gs.cursor = len(gs.results) - 1
		assertDialogFits(t, gs.View(), sz[0], sz[1], "Recall", "› recall-hit-14", "[Esc] Cancel")
	}
}

// terminalFrame is what Bubble Tea's renderer puts on an h-row screen: the
// view split on "\n", keeping only the last h lines.
func terminalFrame(view string, h int) []string {
	lines := strings.Split(strings.ReplaceAll(stripAnsi(view), nbsp, " "), "\n")
	if len(lines) > h {
		lines = lines[len(lines)-h:]
	}
	return lines
}

// TestLocalSearchKeepsTopBorderOnScreen pins the composition of round 1's
// dialog fitter with round 5's search window: Local Search fitted to the
// full height filled all 24 rows at 80x24, centerInScreen's trailing
// newline made it 25 lines, and the renderer dropped the top border. The
// frame on screen must hold one whole box: top border, title, the cursor
// row and the key hints.
func TestLocalSearchKeepsTopBorderOnScreen(t *testing.T) {
	isolateDialogFitHome(t)
	for _, sz := range [][2]int{{60, 15}, {80, 24}, {120, 40}} {
		w, h := sz[0], sz[1]
		for _, n := range []int{11, 25} {
			var items []*session.Instance
			for i := 1; i <= n; i++ {
				items = append(items, &session.Instance{ID: fmt.Sprintf("s%d", i), Title: fmt.Sprintf("session-%02d", i), Tool: "claude"})
			}
			for _, last := range []bool{false, true} {
				s := NewSearch()
				s.SetSize(w, h)
				s.SetItems(items)
				s.Show()
				s.View()
				if last {
					s.cursor = len(s.results) - 1
				}
				want := fmt.Sprintf("› session-%02d", s.cursor+1)
				frame := terminalFrame(s.View(), h)
				screen := strings.Join(frame, "\n")
				// The outer box's top border is the first row drawn; the
				// search input is a second, inner box.
				top := -1
				for i, row := range frame {
					if strings.TrimSpace(row) != "" {
						top = i
						break
					}
				}
				if top < 0 || !strings.HasPrefix(strings.TrimSpace(frame[top]), "╭") || strings.Count(screen, "╭") != strings.Count(screen, "╰") {
					t.Fatalf("%dx%d, %d results, cursor %d: box border clipped on screen:\n%s", w, h, n, s.cursor, screen)
				}
				// The title sits on one of the two rows under the top border.
				if !strings.Contains(strings.Join(frame[top+1:min(top+3, len(frame))], "\n"), "Local Search") {
					t.Errorf("%dx%d, %d results, cursor %d: title is not under the top border:\n%s", w, h, n, s.cursor, screen)
				}
				for _, text := range []string{want, "[Esc] Cancel"} {
					if !strings.Contains(screen, text) {
						t.Errorf("%dx%d, %d results, cursor %d: screen must show %q:\n%s", w, h, n, s.cursor, text, screen)
					}
				}
			}
		}
	}
}
