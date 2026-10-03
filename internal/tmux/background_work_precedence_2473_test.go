package tmux

import (
	"encoding/json"
	"strings"
	"testing"
)

// Issue #2473 review round 2: background work never outranks an open menu or
// an error. The fixtures carry the live workflow row (1/2 · 22s) under:
//
//	workflow-permission-menu.txt  a Bash permission dialog ("Do you want to proceed?")
//	workflow-ask-question.txt     an AskUserQuestion picker ("Enter to select")
//	workflow-auth-401.txt         an expired-token 401 banner at the prompt
//
// A menu blocks the turn on the operator (claudeMenuOutranksBusy, #2185) and
// an error means no progress (#1400), so the frame stays waiting / error.
func TestBackgroundWork2473_MenuAndErrorOutrankWork(t *testing.T) {
	cases := []struct {
		file  string
		frame FrameVerdict
		sub   Substate
	}{
		{"workflow-permission-menu.txt", FrameWaiting, SubstateInteractiveMenu},
		{"workflow-ask-question.txt", FrameWaiting, SubstateInteractiveMenu},
		{"workflow-auth-401.txt", FrameError, SubstateAuth401},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			content := loadBackgroundFixture(t, c.file)
			// Precondition: the frame really does carry work in flight.
			if !ParseClaudeBackgroundWork(content).InFlight() {
				t.Fatal("fixture shows no background work (precondition)")
			}
			if got := ClassifyPaneFrame("claude", content); got != c.frame {
				t.Errorf("frame verdict = %s, want %s", got, c.frame)
			}
			sub, detail, _ := frameSubstate(content)
			if sub != c.sub {
				t.Errorf("substate = %q (detail %q), want %q", sub, detail, c.sub)
			}

			// Every GetStatus branch (main, sustained-activity, recheck,
			// fallback) asks markBackgroundWorkActiveLocked; it must refuse.
			s := &Session{detectedTool: "claude"}
			s.mu.Lock()
			defer s.mu.Unlock()
			trimmed := s.prepareFrame(StripANSI(content))
			if !s.lastBackgroundBlocked {
				t.Error("prepareFrame did not record that the frame outranks background work")
			}
			if s.markBackgroundWorkActiveLocked(trimmed, 0, "t") {
				t.Error("markBackgroundWorkActiveLocked kept the frame green over a menu / error")
			}
			if s.lastStableStatus == "active" {
				t.Error("lastStableStatus was set to active")
			}
		})
	}
}

// The same frames without the menu / banner are still running: the gate is
// specific to what outranks the work.
func TestBackgroundWork2473_GateDoesNotBlockPlainWork(t *testing.T) {
	s := &Session{detectedTool: "claude"}
	s.mu.Lock()
	defer s.mu.Unlock()
	trimmed := s.prepareFrame(StripANSI(loadBackgroundFixture(t, "workflow-running.txt")))
	if s.lastBackgroundBlocked {
		t.Fatal("plain workflow frame recorded as blocked")
	}
	if !s.markBackgroundWorkActiveLocked(trimmed, 0, "t") {
		t.Fatal("plain workflow frame no longer kept green")
	}
}

// A workflow at 0/m keeps "step":0 in the JSON; other kinds omit both.
func TestBackgroundWork2473_JSONKeepsStepZero(t *testing.T) {
	cases := []struct {
		work BackgroundWork
		want string
	}{
		{BackgroundWork{Kind: BackgroundKindWorkflow, Task: "pr6-acceptance", Steps: 2, Elapsed: "2s", Source: "pane"},
			`{"kind":"workflow","task":"pr6-acceptance","step":0,"steps":2,"elapsed":"2s","source":"pane"}`},
		{BackgroundWork{Kind: BackgroundKindWorkflow, Task: "w", Step: 3, Steps: 5, Elapsed: "18m32s", Source: "pane"},
			`{"kind":"workflow","task":"w","step":3,"steps":5,"elapsed":"18m32s","source":"pane"}`},
		{BackgroundWork{Kind: BackgroundKindBash, Task: "2 shells, 1 monitor", Source: "pane"},
			`{"kind":"bash","task":"2 shells, 1 monitor","source":"pane"}`},
	}
	for _, c := range cases {
		b, err := json.Marshal(c.work)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != c.want {
			t.Errorf("json = %s\nwant %s", b, c.want)
		}
		var back BackgroundWork
		if err := json.Unmarshal(b, &back); err != nil || back != c.work {
			t.Errorf("round trip = %+v (%v), want %+v", back, err, c.work)
		}
	}
}

// A draft typed into the input box under the Waiting line, with the workflow
// row below the footer, is still background work in flight.
func TestBackgroundWork2473_TypedDraftUnderWorkflow(t *testing.T) {
	running := loadBackgroundFixture(t, "workflow-running.txt")
	// Claude draws the empty prompt as "❯" + NBSP.
	draft := strings.Replace(running, "\n❯\u00a0\n", "\n❯\u00a0also check the CI once that finishes\n", 1)
	if draft == running {
		t.Fatal("fixture has no empty prompt line (precondition)")
	}
	if got := ClassifyPaneFrame("claude", draft); got != FrameActive {
		t.Errorf("frame verdict = %s, want active", got)
	}
	sub, detail, _ := frameSubstate(draft)
	if sub != SubstateBackgroundWork || detail != "workflow probe-two-agents 1/2 · 22s" {
		t.Errorf("substate = %q detail %q, want background-work with the workflow detail", sub, detail)
	}
}

// proseQuestionFrames are workflow-prose-question.txt and its "Do you want"
// twin: the launch turn ends with a question in Claude's prose, the prompt is
// empty and the workflow row is at 1/2. No menu is open.
func proseQuestionFrames(t *testing.T) map[string]string {
	t.Helper()
	would := loadBackgroundFixture(t, "workflow-prose-question.txt")
	const q = "  Would you like me to run the test suite while it finishes?"
	if !strings.Contains(would, q) {
		t.Fatal("fixture has no prose question (precondition)")
	}
	return map[string]string{
		"would-you-like": would,
		"do-you-want":    strings.Replace(would, q, "  Do you want me to open a PR once both agents report back?", 1),
	}
}

// Issue #2473 review round 3: a reply that ends in a prose question is not an
// open menu. The round-2 gate matched "Would you like" / "Do you want"
// anywhere in the tail, so this frame read waiting/interactive-menu for the
// whole run while the workflow was in flight.
func TestBackgroundWork2473_ProseQuestionDoesNotOutrankWork(t *testing.T) {
	for name, content := range proseQuestionFrames(t) {
		t.Run(name, func(t *testing.T) {
			if hasOpenInteractiveMenu(content) {
				t.Error("prose question read as an open menu")
			}
			if got := ClassifyPaneFrame("claude", content); got != FrameActive {
				t.Errorf("frame verdict = %s, want active", got)
			}
			sub, detail, _ := frameSubstate(content)
			if sub != SubstateBackgroundWork || detail != "workflow probe-two-agents 1/2 · 22s" {
				t.Errorf("substate = %q detail %q, want background-work with the workflow detail", sub, detail)
			}
			s := &Session{detectedTool: "claude"}
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.prepareFrame(StripANSI(content)); s.lastBackgroundBlocked {
				t.Error("prepareFrame recorded the prose question as outranking the work")
			}
		})
	}
}

// The strict open-menu predicate: menu chrome counts on its own, a dialog
// question only with a selected numbered option after it.
func TestHasOpenInteractiveMenu_StrictQuestions(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"prose would-you-like", "⏺ Done.\n\n  Would you like me to push the branch?\n\n❯ \n  ⏵⏵ bypass permissions on", false},
		{"prose do-you-want", "⏺ Done.\n\n  Do you want me to open a PR?\n\n❯ \n  ⏵⏵ bypass permissions on", false},
		{"prose question then numbered list", "  Would you like me to:\n  1. push the branch\n  2. open a PR\n\n❯ \n", false},
		{"permission dialog", " Do you want to proceed?\n ❯ 1. Yes\n   2. No\n", true},
		{"boxed dialog", "│ Do you want to make this edit?\n│ ❯ 1. Yes\n│   2. No\n", true},
		{"cursor moved to option 2", " Would you like to continue?\n   1. Yes\n ❯ 2. No\n", true},
		{"cursor above the question", " ❯ 1. Yes\n Do you want to proceed?\n", false},
		{"chrome: Esc to cancel", " Bash command\n\n Esc to cancel · Tab to amend\n", true},
		{"chrome: Enter to select", "❯ 1. staging\n  2. production\n\nEnter to select · ↑/↓ to navigate · Esc to cancel\n", true},
		{"chrome: tell Claude", "❯ Yes\n  No, and tell Claude what to do differently\n", true},
		{"chrome: survey", "  How is Claude doing this session? (optional)\n  1: Bad    2: Fine   3: Good   0: Dismiss\n", true},
	}
	for _, c := range cases {
		if got := hasOpenInteractiveMenu(c.content); got != c.want {
			t.Errorf("%s: hasOpenInteractiveMenu = %v, want %v", c.name, got, c.want)
		}
	}
}

// A prose question with no background work is an idle prompt, not a menu.
func TestClassifySubstate_ProseQuestionIsIdlePrompt(t *testing.T) {
	d := NewPromptDetector("claude")
	content := "⏺ Pushed the branch.\n\n  Would you like me to open a PR as well?\n\n────────\n❯ \n────────\n  ⏵⏵ bypass permissions on (shift+tab to cycle)\n"
	if got := d.ClassifySubstate(content); got != SubstateIdleAtEmptyPrompt {
		t.Errorf("prose question at the prompt = %q, want %q", got, SubstateIdleAtEmptyPrompt)
	}
}
