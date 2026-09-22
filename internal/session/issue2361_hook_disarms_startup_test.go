package session

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// newIssue2361Instance returns a Claude instance on a real, inert tmux pane
// (never renders a prompt or busy signal) whose startup clock is already past
// the 2-minute window, bound to Claude session "owner-session".
func newIssue2361Instance(t *testing.T, name string) *Instance {
	t.Helper()
	skipIfNoTmuxBinary(t)

	dir := t.TempDir()
	inst := NewInstance(name, dir)
	inst.Tool = "claude"
	inst.ClaudeSessionID = "owner-session"
	inst.tmuxSession = tmux.NewSession(inst.Title, dir)
	if err := inst.tmuxSession.Start("sleep 300"); err != nil {
		t.Fatalf("start inert pane: %v", err)
	}
	t.Cleanup(func() { _ = inst.tmuxSession.Kill() })

	inst.lastStartTime = time.Now().Add(-3 * time.Minute)
	inst.tmuxSession.SetStartupAtForTest(time.Now().Add(-3 * time.Minute))
	return inst
}

// The owning agent's hook ends the startup phase, so when the hook goes stale
// and UpdateStatus falls through to tmux.GetStatus, the pane is not expired.
func TestIssue2361_OwnHookDisarmsStartupTimeout(t *testing.T) {
	inst := newIssue2361Instance(t, "test-2361-own-hook")

	// Past hookFastPathWindow, so UpdateStatus falls through to GetStatus, but
	// after the pane's startup clock began.
	inst.UpdateHookStatus(&HookStatus{
		Status:    "waiting",
		Event:     "Stop",
		SessionID: "owner-session",
		Cwd:       inst.ProjectPath,
		UpdatedAt: time.Now().Add(-150 * time.Second),
	})

	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if got := inst.GetStatusThreadSafe(); got == StatusError {
		t.Fatalf("status = %q: startup timeout fired on a pane whose agent reported a hook", got)
	}
}

// A foreign ephemeral's hook (a `claude -p` child that inherited our
// AGENTDECK_INSTANCE_ID, cwd outside the instance) is rejected and must not
// disarm the watchdog for a pane that never became interactive.
func TestIssue2361_ForeignHookDoesNotDisarmStartupTimeout(t *testing.T) {
	inst := newIssue2361Instance(t, "test-2361-foreign-hook")

	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		Event:     "UserPromptSubmit",
		SessionID: "foreign-ephemeral",
		Cwd:       t.TempDir(),
		UpdatedAt: time.Now().Add(-150 * time.Second),
	})

	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if got := inst.GetStatusThreadSafe(); got != StatusError {
		t.Fatalf("status = %q, want %q: a rejected foreign hook disarmed the startup timeout", got, StatusError)
	}
}
