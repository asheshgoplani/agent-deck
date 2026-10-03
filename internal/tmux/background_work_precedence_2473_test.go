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
