package ui

// TUI evaluator — SEAM B: teatest (Bubble Tea test harness)
//
// teatest boots a real tea.Program, connected to io.Reader/io.Writer
// buffers instead of a real terminal. Tests send tea.Msg values via
// tm.Send(...), read rendered output via tm.Output(), and recover the
// final model via tm.FinalModel() to assert on struct state.
//
// This seam catches three things Seam A cannot:
//   - Message routing through the real tea runtime (not just Update).
//   - tea.Batch / tea.Cmd sequencing ordering bugs.
//   - Rendered output regressions via WaitFor / RequireEqualOutput.
//
// It's slower than Seam A (~50-200ms per test) and still doesn't see a
// real terminal (no real vt100 — output is a byte buffer). For real
// terminal behavior use Seam C.
//
// Gotcha: teatest calls model.Init(). Home.Init() spawns storage
// watchers, status workers, and tickers. Tests pin the seam against a
// lightweight wrapper (seamBTestWrapper) that provides a no-op Init and
// delegates Update/View to the real Home. This is the recommended
// pattern — do NOT hand NewHome() directly to teatest.

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
)

// seamBTestWrapper is a tea.Model that delegates to Home but has a
// no-op Init — keeping teatest from spawning real workers/tickers.
// All production Update/View behavior is exercised unchanged.
type seamBTestWrapper struct {
	home *Home
}

func (w *seamBTestWrapper) Init() tea.Cmd { return nil }

func (w *seamBTestWrapper) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m, cmd := w.home.Update(msg)
	w.home = m.(*Home)
	return w, cmd
}

func (w *seamBTestWrapper) View() string {
	return w.home.View()
}

// TestSeamB_HelpOverlay_ViaTeatest drives the real Bubble Tea runtime
// and asserts that pressing '?' renders the help overlay content.
//
// Pattern to copy:
//  1. Build a Home (seamBNewHome below strips storage side effects).
//  2. Wrap in seamBTestWrapper to silence Init.
//  3. teatest.NewTestModel, Send keys, tm.Quit(), read FinalOutput.
//  4. Assert on rendered bytes OR on tm.FinalModel().(*seamBTestWrapper).home.
func TestSeamB_HelpOverlay_ViaTeatest(t *testing.T) {
	w := &seamBTestWrapper{home: seamBNewHome()}

	tm := teatest.NewTestModel(t, w, teatest.WithInitialTermSize(140, 50))
	// Drive a window-size msg so layouts have real dimensions.
	tm.Send(tea.WindowSizeMsg{Width: 140, Height: 50})
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})

	// Quit cleanly so FinalOutput is closed.
	tm.Send(tea.QuitMsg{})
	if err := tm.Quit(); err != nil {
		// Quit can return errors if program already exited — non-fatal for POC.
		t.Logf("tm.Quit: %v", err)
	}

	final := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second)).(*seamBTestWrapper)
	if !final.home.helpOverlay.IsVisible() {
		t.Fatalf("Seam B failure: teatest drove '?' but helpOverlay not visible. " +
			"This means the real tea runtime didn't route the KeyMsg into Update. " +
			"Either teatest is misconfigured or Home's msg routing has regressed.")
	}

	// Demonstrate output-based assertion too: find a known help overlay string.
	out, _ := io.ReadAll(tm.FinalOutput(t, teatest.WithFinalTimeout(2*time.Second)))
	// Don't pin exact bytes (brittle) — just prove the harness captured SOMETHING.
	if len(out) == 0 {
		t.Fatalf("Seam B: no output captured — teatest reader returned empty. " +
			"Check that the test model's View() was rendered.")
	}
	_ = bytes.TrimSpace // keep bytes import even if we don't match strictly
}

// TestSeamB_Issue666_ResolverSurvivesFullRuntime is the teatest-level
// complement to TestIssue666_* (issue666_tui_test.go). It boots a real
// tea runtime with a Window-cursor scoped Home and sends innocuous keys,
// then asserts groupScope survives — so that if the user DID trigger a
// global search import right after, resolveNewSessionGroup would still
// rescue correctly.
//
// This seam's value over Seam A: it exercises the full Msg-routing
// pipeline (tea.Batch ordering, program-managed state). A regression
// that only appears under real tea runtime (not Seam A's synchronous
// Update call) would surface here.
func TestSeamB_Issue666_ResolverSurvivesFullRuntime(t *testing.T) {
	h := seamBNewHome()
	h.groupScope = "agent-deck"
	h.flatItems = []session.Item{
		{Type: session.ItemTypeWindow, WindowName: "tmux-window-0"},
	}
	h.cursor = 0
	w := &seamBTestWrapper{home: h}

	tm := teatest.NewTestModel(t, w, teatest.WithInitialTermSize(140, 50))
	tm.Send(tea.WindowSizeMsg{Width: 140, Height: 50})
	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'?'}},
		{Type: tea.KeyEsc},
	} {
		tm.Send(k)
	}
	tm.Send(tea.QuitMsg{})
	_ = tm.Quit()

	final := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second)).(*seamBTestWrapper)
	gs := final.home.groupScope
	if gs != "agent-deck" {
		t.Fatalf("issue #666 regression at teatest seam: groupScope=%q after "+
			"innocuous key sequence; want agent-deck. Empty/wrong scope here "+
			"means resolveNewSessionGroup would silently fall back to DefaultGroupPath "+
			"on a subsequent global-search import (home.go:4836).", gs)
	}
	if final.home.resolveNewSessionGroup() != "agent-deck" {
		t.Fatalf("issue #666: resolveNewSessionGroup did not preserve scope through teatest runtime.")
	}
}

// seamBNewHome is the test-time Home constructor. Provides enough
// defaults that View() doesn't nil-panic, but skips storage / tmux /
// tickers / workers. When you add fields to Home that View() or Update()
// dereference unconditionally, add them here too.
func seamBNewHome() *Home {
	h := &Home{
		search:               NewSearch(),
		newDialog:            NewNewDialog(),
		groupDialog:          NewGroupDialog(),
		forkDialog:           NewForkDialog(),
		confirmDialog:        NewConfirmDialog(),
		helpOverlay:          NewHelpOverlay(),
		mcpDialog:            NewMCPDialog(),
		editPathsDialog:      NewEditPathsDialog(),
		skillDialog:          NewSkillDialog(),
		setupWizard:          NewSetupWizard(),
		settingsPanel:        NewSettingsPanel(),
		analyticsPanel:       NewAnalyticsPanel(),
		geminiModelDialog:    NewGeminiModelDialog(),
		sessionPickerDialog:  NewSessionPickerDialog(),
		worktreeFinishDialog: NewWorktreeFinishDialog(),
		feedbackDialog:       NewFeedbackDialog(),
		globalSearch:         NewGlobalSearch(),
		watcherPanel:         NewWatcherPanel(),
		notesEditor:          newNotesEditor(),
		groupTree:            session.NewGroupTree([]*session.Instance{}),
		instanceByID:         make(map[string]*session.Instance),
		previewCache:         make(map[string]string),
		previewCacheTime:     make(map[string]time.Time),
		analyticsCache:       make(map[string]*session.SessionAnalytics),
		geminiAnalyticsCache: make(map[string]*session.GeminiSessionAnalytics),
		analyticsCacheTime:   make(map[string]time.Time),
		launchingSessions:    make(map[string]time.Time),
		resumingSessions:     make(map[string]time.Time),
		mcpLoadingSessions:   make(map[string]time.Time),
		forkingSessions:      make(map[string]time.Time),
		creatingSessions:     make(map[string]*CreatingSession),
		lastLogActivity:      make(map[string]time.Time),
		windowsCollapsed:     make(map[string]bool),
		worktreeDirtyCache:   make(map[string]bool),
		worktreeDirtyCacheTs: make(map[string]time.Time),
		hotkeys:              make(map[string]string),
		hotkeyLookup:         make(map[string]string),
		blockedHotkeys:       make(map[string]bool),
		boundKeys:            make(map[string]string),
		pendingTitleChanges:  make(map[string]string),
		clearOnCompactSent:   make(map[string]time.Time),
		lastPersistedStatus:  make(map[string]string),
		width:                140,
		height:               50,
		lastClickIndex:       -1,
	}
	h.sessionRenderSnapshot.Store(make(map[string]sessionRenderState))
	return h
}
