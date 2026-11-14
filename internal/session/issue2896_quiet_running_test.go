// Issue #2896 (reporter koenzhao): a pi session sat at "running" for four
// days at an idle prompt. Its last hook event was a turn_end (waiting); the
// hook fast path dropped it after hookFastPathWindow (2 minutes) and the
// pane-derived verdict for the quiet pane said running from then on.
//
// A pi turn_end/session_start ("waiting") hook now holds the status until the
// next hook event or new pane activity after the freshness window. Claude and
// Codex keep their windows.
package session

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// startQuietPiPane starts a pi-labelled pane whose frame the pane detector
// reads as busy (a "[running]" marker left above pi's idle footer), which is
// the verdict the reported session was stuck on. The hook event under test is
// the newer evidence.
func startQuietPiPane(t *testing.T, name, tool string) *Instance {
	t.Helper()
	inst := NewInstanceWithTool(name, "/tmp", "shell")
	inst.Command = `printf '[running] scout\n\n↑5.9k ↓77 $0.017\n'; sleep 60`
	if err := inst.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	t.Cleanup(func() { _ = inst.Kill() })
	time.Sleep(2 * time.Second)
	inst.Tool = tool
	inst.loadCustomPatternsFromConfig() // the pane reads with the tool's patterns
	return inst
}

// setPaneActivity makes the pane report its last activity at the given time.
func setPaneActivity(t *testing.T, at time.Time) {
	t.Helper()
	orig := hookPinPaneActivity
	hookPinPaneActivity = func(*tmux.Session) int64 { return at.Unix() }
	t.Cleanup(func() { hookPinPaneActivity = orig })
}

// The reported timeline: turn_end four hours ago, no pane activity since.
func TestIssue2896_PiTurnEndHoldsWaitingOnQuietPane(t *testing.T) {
	skipIfNoTmuxBinary(t)
	inst := startQuietPiPane(t, "test-2896-quiet", "pi")

	stop := time.Now().Add(-4 * time.Hour)
	setPaneActivity(t, stop)
	inst.UpdateHookStatus(&HookStatus{Status: "waiting", Event: "turn_end", UpdatedAt: stop})

	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("UpdateStatus() failed: %v", err)
	}
	if inst.Status != StatusWaiting {
		t.Fatalf("pi with a 4h-old turn_end and a quiet pane: status %v, want %v", inst.Status, StatusWaiting)
	}
}

// New pane activity after the freshness window ends the hold: the pane
// decides again (here it reads busy).
func TestIssue2896_PiTurnEndYieldsToNewPaneActivity(t *testing.T) {
	skipIfNoTmuxBinary(t)
	inst := startQuietPiPane(t, "test-2896-activity", "pi")

	stop := time.Now().Add(-4 * time.Hour)
	setPaneActivity(t, time.Now())
	inst.UpdateHookStatus(&HookStatus{Status: "waiting", Event: "turn_end", UpdatedAt: stop})

	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("UpdateStatus() failed: %v", err)
	}
	if inst.Status != StatusRunning {
		t.Fatalf("pi turn_end with pane activity since: status %v, want %v (pane decides)", inst.Status, StatusRunning)
	}
}

// Only a terminal event holds. A stale turn_start (running) and a stale
// session_shutdown (dead: pi may have dropped to a fallback shell) still go
// back to the pane after the window.
func TestIssue2896_PiNonWaitingHookStillExpires(t *testing.T) {
	testutil.SkipShort(t, "drives real tmux panes for several seconds; runs in CI without -short")
	skipIfNoTmuxBinary(t)
	for _, hs := range []struct{ status, event string }{
		{"running", "turn_start"},
		{"dead", "session_shutdown"},
	} {
		t.Run(hs.event, func(t *testing.T) {
			inst := startQuietPiPane(t, "test-2896-"+hs.status, "pi")
			at := time.Now().Add(-4 * time.Hour)
			setPaneActivity(t, at)
			inst.UpdateHookStatus(&HookStatus{Status: hs.status, Event: hs.event, UpdatedAt: at})
			if err := inst.UpdateStatus(); err != nil {
				t.Fatalf("UpdateStatus() failed: %v", err)
			}
			if inst.Status != StatusRunning {
				t.Fatalf("stale pi %s hook: status %v, want %v (pane decides)", hs.event, inst.Status, StatusRunning)
			}
		})
	}
}

// Claude and Codex keep their freshness windows, and the hold is pi-only.
func TestIssue2896_ClaudeAndCodexWindowsUnchanged(t *testing.T) {
	if got := hookFastPathFreshnessForTool("pi", "waiting"); got != hookFastPathWindow {
		t.Errorf("pi waiting freshness = %v, want %v", got, hookFastPathWindow)
	}
	if got := hookFastPathFreshnessForTool("claude", "waiting"); got != hookFastPathWindow {
		t.Errorf("claude waiting freshness = %v, want %v", got, hookFastPathWindow)
	}
	if got := hookFastPathFreshnessForTool("codex", "waiting"); got != codexHookWaitingFastPathWindow {
		t.Errorf("codex waiting freshness = %v, want %v", got, codexHookWaitingFastPathWindow)
	}
	// No hold for them either, even on a pane quiet since the event.
	setPaneActivity(t, time.Now().Add(-4*time.Hour))
	for _, tool := range []string{"claude", "codex", "gemini"} {
		inst := NewInstanceWithTool("hold-"+tool, "/tmp/test", tool)
		inst.hookStatus = "waiting"
		inst.hookLastUpdate = time.Now().Add(-4 * time.Hour)
		if inst.hookHeldByTerminalEventLocked() {
			t.Errorf("%s: a 4h-old waiting hook must not be held", tool)
		}
	}
}
