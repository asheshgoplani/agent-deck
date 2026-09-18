package session

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// TestIssue2185_SessionShowJSONReportsInteractiveMenuSubstate closes the gap
// the #2185 verification flagged: internal/tmux's ClassifySubstate unit test
// proves the classifier itself returns the right enum for an open
// AskUserQuestion menu, but nothing exercised the full pipeline an external
// consumer of `agent-deck session show --json` actually observes — Instance
// status/substate populated from a live tmux pane capture.
//
// Reproduces the reporter's pane text (an open AskUserQuestion picker plus
// its "Enter to select · Tab/Arrow keys to navigate · Esc to cancel" footer)
// in a real tmux pane and asserts the Instance-level accessors (what JSON
// serialization for `session show --json` reads) report Status="waiting"
// and Substate="interactive-menu" — not idle-at-empty-prompt, which a
// supervisor reading it would misread as "nothing is happening" when work
// is actually blocked on an unanswered question.
func TestIssue2185_SessionShowJSONReportsInteractiveMenuSubstate(t *testing.T) {
	skipIfNoTmuxBinary(t)

	inst := NewInstance("test-2185-session-layer", "/tmp")
	inst.Tool = "claude"
	inst.tmuxSession = tmux.NewSession(inst.Title, "/tmp")
	// The fake pane script below is a plain shell one-liner, not a real
	// `claude` invocation, so tmux's command-based tool inference would miss
	// it; declare the tool explicitly the same way Instance's real spawn
	// path does via SetDetectPatterns.
	inst.tmuxSession.SetDetectPatterns("claude", nil)

	// Render the AskUserQuestion menu text once, then sit still (like a
	// real Claude pane blocked on an unanswered question) so the pane is
	// stable when captured.
	script := `printf 'Which approach should I take?\n' ` +
		`&& printf '\xe2\x9d\xaf 1. Option A\n' ` +
		`&& printf '  2. Option B\n\n' ` +
		`&& printf 'Enter to select \xc2\xb7 Tab/Arrow keys to navigate \xc2\xb7 Esc to cancel\n' ` +
		`&& sleep 300`
	if err := inst.tmuxSession.Start(script); err != nil {
		t.Fatalf("start fake AskUserQuestion pane: %v", err)
	}
	t.Cleanup(func() { _ = inst.tmuxSession.Kill() })
	inst.lastStartTime = time.Now().Add(-3 * time.Second)

	var status Status
	var substate tmux.Substate
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := inst.UpdateStatus(); err != nil {
			t.Fatalf("UpdateStatus: %v", err)
		}
		status = inst.GetStatusThreadSafe()
		substate = inst.Substate()
		if substate == tmux.SubstateInteractiveMenu {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if status != StatusWaiting {
		t.Fatalf("session-layer status for an open AskUserQuestion menu = %q, want %q", status, StatusWaiting)
	}
	if substate != tmux.SubstateInteractiveMenu {
		t.Fatalf("session-layer substate = %q, want %q (this is what `session show --json` reports as \"substate\")", substate, tmux.SubstateInteractiveMenu)
	}
}
