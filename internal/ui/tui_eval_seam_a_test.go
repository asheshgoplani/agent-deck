package ui

// TUI evaluator — SEAM A: model-level (direct method / Update dispatch)
//
// This is the fastest, most deterministic seam. It treats Home as a plain
// struct + tea.Model. Tests instantiate a minimal Home, set cursor / scope
// / flatItems by hand, then either:
//
//   (A1) call the method under test directly
//   (A2) send a real tea.KeyMsg through h.Update(...) and inspect the
//        returned (model, cmd) plus any mutated struct state
//
// No PTY, no tmux, no rendering. Cannot catch render regressions, but
// catches ~90% of logic bugs in milliseconds. See internal/ui/TUI_TESTS.md
// for when to pick this seam.
//
// The canonical issue #666 regression at this seam lives in
// issue666_tui_test.go (resolver contract + storage round-trip). This file
// adds a third demonstration: key-routing through Home.Update.

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

// TestSeamA_KeyDispatch_HelpOverlayToggle is the canonical Seam A example:
// drive a real tea.KeyMsg through Home.Update and assert on the resulting
// model state. No side effects, no PTY.
//
// Pattern to copy for new TUI bug reproductions:
//  1. Build a minimal Home (this file shows the required fields).
//  2. Send the exact tea.KeyMsg that triggers the symptom.
//  3. Assert on the mutated struct state.
func TestSeamA_KeyDispatch_HelpOverlayToggle(t *testing.T) {
	h := newSeamATestHome()
	if h.helpOverlay.IsVisible() {
		t.Fatalf("fixture broken: help overlay should start hidden")
	}

	newModel, _ := h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	got := newModel.(*Home)

	if !got.helpOverlay.IsVisible() {
		t.Fatalf("Seam A failure: '?' key did not toggle help overlay. " +
			"Either key routing changed or the overlay stopped reacting to Show(). " +
			"Reproduce manually: run agent-deck, press '?' — should open help.")
	}
}

// TestSeamA_Issue666_KeyDispatch_DoesNotZeroGroupScope is a key-dispatch
// regression pin complementing TestIssue666_* in issue666_tui_test.go.
// It proves that driving keys through Home.Update with the cursor on a
// Window item (the exact condition that used to trigger empty GroupPath)
// never clobbers the groupScope.
//
// This guards against a future refactor that might set h.groupScope = ""
// as a side effect of some key handler — which would re-open the #666
// failure mode by making resolveNewSessionGroup fall back from scope to
// DefaultGroupPath silently.
func TestSeamA_Issue666_KeyDispatch_DoesNotZeroGroupScope(t *testing.T) {
	h := newSeamATestHome()
	h.groupScope = "agent-deck"
	h.flatItems = []session.Item{
		{Type: session.ItemTypeWindow, WindowName: "tmux-window-0"},
	}
	h.cursor = 0

	// Drive a sequence of innocuous keys. None of these should ever touch groupScope.
	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'?'}}, // help on
		{Type: tea.KeyEsc},                       // help off
		{Type: tea.KeyDown},
		{Type: tea.KeyUp},
	} {
		m, _ := h.Update(k)
		h = m.(*Home)
	}

	if h.groupScope != "agent-deck" {
		t.Fatalf("issue #666 regression: groupScope zeroed after innocuous key sequence "+
			"(was %q, want %q). Empty scope would make resolveNewSessionGroup fall through "+
			"to DefaultGroupPath on the next global-search import.", h.groupScope, "agent-deck")
	}
}

// newSeamATestHome constructs the minimal Home required to drive Update
// without storage, tmux, or notification side effects. If a field is
// missing here, add it — don't reach for NewHome() (which spawns
// storage + watchers + workers, polluting tests).
func newSeamATestHome() *Home {
	return &Home{
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
		width:                120,
		height:               40,
	}
}
