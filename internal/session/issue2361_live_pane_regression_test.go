package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// issue2361ClaudeIdleFrame is a Claude pane idle at its prompt after a
// finished turn: what the pane in #2361 showed when the watchdog killed it.
const issue2361ClaudeIdleFrame = `I've made the following changes:
1. Added the main function
2. Imported fmt package

✻ Cooked for 32s

──────────────────────────────────────────────────────────────
❯
──────────────────────────────────────────────────────────────
`

// issue2361StuckFrame is the stuck-pane capture quoted in #1892: the process
// stays alive but never renders a busy signal or a prompt.
const issue2361StuckFrame = `  Session is starting — showing its transcript until it appears. Ctrl+Z t
^[zfadsffa  ^[^[^[^[^[^[^[^[^[[A^[[Aq^[[A^[[Aqqqqqqqq
`

// startIssue2361Pane starts a Claude instance on a real tmux pane that prints
// frame and then stays alive, with its startup clock started 3 minutes ago
// (past tmux's 2-minute startupStateWindow). It waits until marker is on
// screen so the probe sees what a real pane would show.
func startIssue2361Pane(t *testing.T, name, frame, marker string) *Instance {
	t.Helper()
	skipIfNoTmuxBinary(t)

	dir := t.TempDir()
	framePath := filepath.Join(dir, "frame.txt")
	if err := os.WriteFile(framePath, []byte(frame), 0o600); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	inst := NewInstance(name, dir)
	inst.Tool = "claude"
	inst.ClaudeSessionID = "owner-session"
	inst.tmuxSession = tmux.NewSession(inst.Title, dir)
	if err := inst.tmuxSession.Start("cat " + framePath + "; sleep 300"); err != nil {
		t.Fatalf("start pane: %v", err)
	}
	t.Cleanup(func() { _ = inst.tmuxSession.Kill() })
	// Detection infers the agent from Command; Start recorded the cat/sleep line.
	inst.tmuxSession.Command = "claude"

	deadline := time.Now().Add(3 * time.Second)
	for {
		pane, err := inst.tmuxSession.CapturePaneFresh()
		if err == nil && strings.Contains(tmux.StripANSI(pane), marker) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane never showed %q (err=%v):\n%s", marker, err, pane)
		}
		time.Sleep(20 * time.Millisecond)
	}

	startedAt := time.Now().Add(-3 * time.Minute)
	inst.lastStartTime = startedAt
	inst.tmuxSession.SetStartupAtForTest(startedAt)
	return inst
}

func issue2361PanePID(t *testing.T, inst *Instance) int {
	t.Helper()
	pid, err := inst.tmuxSession.PanePID()
	if err != nil {
		t.Fatalf("pane pid: %v", err)
	}
	return pid
}

// assertIssue2361NotExpired fails if the startup watchdog replaced the pane.
func assertIssue2361NotExpired(t *testing.T, inst *Instance, wantPID int) {
	t.Helper()
	if got := inst.GetStatusThreadSafe(); got == StatusError {
		t.Fatalf("status = %q: the startup watchdog expired a live Claude pane (#2361)", got)
	}
	if pid := issue2361PanePID(t, inst); pid != wantPID {
		t.Fatalf("pane respawned (pid %d -> %d): the startup watchdog killed a live Claude pane (#2361)", wantPID, pid)
	}
	raw, err := inst.tmuxSession.CapturePaneFresh()
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if strings.Contains(strings.ToLower(tmux.StripANSI(raw)), "timed out") {
		t.Fatalf("pane shows the startup-timeout hold (#2361):\n%s", raw)
	}
}

// TestIssue2361_LiveClaudePaneSurvivesHookLapse replays the #2361 timeline
// through the TUI's poll sequence: SessionStart arrives while the hook fast
// path is fresh (UpdateStatus never reaches tmux.GetStatus), the session
// goes idle at its prompt, and the first poll after the hook lapses past
// hookFastPathWindow falls through to GetStatus. That poll must not expire
// the pane.
func TestIssue2361_LiveClaudePaneSurvivesHookLapse(t *testing.T) {
	inst := startIssue2361Pane(t, "test-2361-live-hook-lapse", issue2361ClaudeIdleFrame, "Cooked for 32s")
	pid := issue2361PanePID(t, inst)

	// SessionStart, after the pane's clock began and inside the fast path.
	inst.UpdateHookStatus(&HookStatus{
		Status:    "waiting",
		Event:     "SessionStart",
		SessionID: "owner-session",
		Cwd:       inst.ProjectPath,
		UpdatedAt: time.Now().Add(-100 * time.Second),
	})
	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("UpdateStatus (hook fast path): %v", err)
	}
	assertIssue2361NotExpired(t, inst, pid)

	// No hook for 2+ minutes: the next poll falls through to GetStatus.
	inst.mu.Lock()
	inst.hookLastUpdate = time.Now().Add(-hookFastPathWindow - 30*time.Second)
	inst.mu.Unlock()
	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("UpdateStatus (hook lapsed): %v", err)
	}
	assertIssue2361NotExpired(t, inst, pid)
}

// TestIssue2361_LiveClaudePaneWithoutHookSurvivesOverdueClock covers a
// process that never sees the hook (a CLI or remote probe that starts after
// the hook file aged out): the pane itself shows Claude at its prompt, so an
// overdue startup clock must resolve instead of expiring it.
func TestIssue2361_LiveClaudePaneWithoutHookSurvivesOverdueClock(t *testing.T) {
	inst := startIssue2361Pane(t, "test-2361-live-no-hook", issue2361ClaudeIdleFrame, "Cooked for 32s")
	pid := issue2361PanePID(t, inst)

	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	assertIssue2361NotExpired(t, inst, pid)
}

// TestIssue2361_DeadStartupStillTimesOut is the other half of the contract:
// a pane that never became interactive (no hook, stuck #1892 content) must
// still be replaced with the timeout hold.
func TestIssue2361_DeadStartupStillTimesOut(t *testing.T) {
	inst := startIssue2361Pane(t, "test-2361-dead-startup", issue2361StuckFrame, "Session is starting")

	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if got := inst.GetStatusThreadSafe(); got != StatusError {
		t.Fatalf("status = %q, want %q: a dead startup no longer times out", got, StatusError)
	}
	var pane string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := inst.tmuxSession.CapturePaneFresh()
		pane = strings.ToLower(tmux.StripANSI(raw))
		if err == nil && strings.Contains(pane, "timed out") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("dead startup pane never showed the timeout hold:\n%s", pane)
}
