package session

// Issue #911 — REQ-7 reopened (v1.9.x stability sweep).
//
// Custom-command sessions launched via wrapper scripts (start-conductor.sh,
// launch-subagent.sh, etc.) end up with claude_session_id="" because the
// spawn happens outside agent-deck's happy-path capture. The registry then
// treats them as broken, refusing restart even when the underlying tmux pane
// is alive and Claude is healthy.
//
// The structural fix surface lives in CanRestart (instance.go:5119). The
// opencode and codex branches (lines 5137 and 5148) explicitly allow restart
// of empty-ID sessions because spawn-time capture isn't always possible. The
// Claude branch (line 5126) requires a non-empty ClaudeSessionID, so a
// custom-command Claude session falls all the way to the dead-or-error
// fallback (line 5158) — meaning a healthy alive session reports
// "unrestartable", the user-visible symptom #911 tracks.
//
// See ~/.claude/projects/-home-ashesh-goplani--agent-deck/memory/
// conductor_restart_history_loss.md for the structural background.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRegistry_CustomCommand_NoFalseError_RegressionFor911 pins the contract:
// a Claude session with a custom Command (wrapper script) and an empty
// ClaudeSessionID, whose tmux pane is alive, MUST behave like a healthy
// session for registry-level capability checks — same as opencode/codex
// without session IDs.
func TestRegistry_CustomCommand_NoFalseError_RegressionFor911(t *testing.T) {
	// Use skipIfNoTmuxBinary (not skipIfNoTmuxServer): the latter is a
	// pre-bootstrap legacy guard that skips when only the TestMain-spawned
	// bootstrap session exists. This new test runs against the isolated
	// socket and creates its own session via Start(), so the bootstrap is
	// sufficient.
	skipIfNoTmuxBinary(t)

	inst := NewInstanceWithTool("test-911-custom-cmd", "/tmp", "claude")
	// Tool=claude with a custom Command (not the default "claude") models
	// the launch-subagent.sh / start-conductor.sh class of spawns that
	// bypass happy-path session-id capture.
	inst.Command = "sleep 30"
	require.Empty(t, inst.ClaudeSessionID,
		"precondition: REQ-7 scenario starts with empty claude_session_id "+
			"because spawn happens outside happy-path capture")

	err := inst.Start()
	require.NoError(t, err, "Start() must succeed for custom-command claude session")
	defer func() { _ = inst.Kill() }()

	// Wait past the 1.5s instance-level grace period so UpdateStatus
	// performs a real status derivation rather than the StatusStarting hold.
	time.Sleep(2 * time.Second)

	err = inst.UpdateStatus()
	require.NoError(t, err, "UpdateStatus() must succeed against an alive tmux pane")

	// Status check assertion: a healthy custom-command Claude session must
	// not be classified as error after a status check. This guards the
	// scenario where a stale Status=Error survives a cascade and the next
	// status refresh fails to clear it because of the empty session_id.
	assert.NotEqual(t, StatusError, inst.Status,
		"REQ-7 #911: custom-command claude session with alive tmux pane "+
			"must not be StatusError after a status check; got %s", inst.Status)

	// Registry capability assertion (RED on main): CanRestart returns false
	// for this scenario because the claude branch at instance.go:5126
	// requires non-empty ClaudeSessionID. The opencode and codex branches
	// (lines 5137, 5148) explicitly allow empty-ID restart. The fix wires
	// a parallel claude branch so custom-command Claude sessions get the
	// same treatment — the cascade-recoverable contract REQ-7 promises.
	require.True(t, inst.CanRestart(),
		"REQ-7 #911 RED: CanRestart() returned false for a healthy custom-"+
			"command Claude session with alive tmux. Opencode/codex sessions "+
			"with empty session IDs are explicitly restartable (instance.go:"+
			"5137/5148). The Claude branch must mirror that contract so "+
			"custom-command sessions launched via wrapper scripts (where "+
			"spawn-time capture is intentionally null) are not treated as "+
			"broken by the registry.")
}
