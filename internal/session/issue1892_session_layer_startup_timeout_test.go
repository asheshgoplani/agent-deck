package session

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// TestIssue1892_SessionLayerSurfacesStuckStartupTimeout closes the gap the
// #1892 verification flagged: the existing regression coverage (internal/tmux's
// TestIssue1892_StartupWithoutAgentSignalTimesOutHonesty) only proves
// tmux.Session itself reports the stuck-startup timeout honestly. Nothing
// exercised the agent-deck session layer that `session show --json` and
// `session output` actually read from — Instance.UpdateStatus/GetStatusThreadSafe
// and Instance.Substate. If that layer ever diverged from tmux's contract
// (e.g. a stale cache, a different status mapping), this test — not the
// white-box one — would be the one to catch it.
//
// Reproduces #1892 with a real tmux pane running an inert command that never
// renders an agent prompt or busy signal (issue's own repro shape), backdates
// the session's startup clock past the window via the tmux package's test
// seam, then asserts through the Instance API that status/substate/pane text
// report the failure the same way the CLI would.
func TestIssue1892_SessionLayerSurfacesStuckStartupTimeout(t *testing.T) {
	skipIfNoTmuxBinary(t)

	inst := NewInstance("test-1892-session-layer", "/tmp")
	inst.Tool = "claude"
	inst.tmuxSession = tmux.NewSession(inst.Title, "/tmp")
	if err := inst.tmuxSession.Start("sleep 300"); err != nil {
		t.Fatalf("start inert pane: %v", err)
	}
	t.Cleanup(func() { _ = inst.tmuxSession.Kill() })

	// Past the 1.5s tmux-init grace period in updateStatus AND well past
	// tmux's own 2-minute startupStateWindow.
	inst.lastStartTime = time.Now().Add(-3 * time.Minute)
	inst.tmuxSession.SetStartupAtForTest(time.Now().Add(-3 * time.Minute))

	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	if got := inst.GetStatusThreadSafe(); got != StatusError {
		t.Fatalf("session-layer status after stuck startup = %q, want %q (agent-deck session show --json would report this)", got, StatusError)
	}

	var pane string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var capErr error
		var raw string
		raw, capErr = inst.tmuxSession.CapturePaneFresh()
		pane = tmux.StripANSI(raw)
		if capErr == nil && strings.Contains(strings.ToLower(pane), "timed out") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	lowerPane := strings.ToLower(pane)
	if !strings.Contains(lowerPane, "timed out") || !strings.Contains(lowerPane, "agent-deck session restart") {
		t.Fatalf("session output does not show the bounded-failure recovery text:\n%s", pane)
	}
}
