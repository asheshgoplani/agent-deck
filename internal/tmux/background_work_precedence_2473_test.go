package tmux

import "testing"

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
