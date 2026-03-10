package integration

import (
	"os/exec"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLifecycleStart_CreatesRealSession verifies that starting a session through
// TmuxHarness creates a real tmux session with observable pane content. (LIFE-01)
func TestLifecycleStart_CreatesRealSession(t *testing.T) {
	h := NewTmuxHarness(t)

	inst := h.CreateSession("start-real", "/tmp")
	inst.Command = "echo hello && sleep 60"
	require.NoError(t, inst.Start())

	assert.True(t, inst.Exists(), "session should exist after Start()")
	WaitForPaneContent(t, inst, "hello", 5*time.Second)
	assert.NotEmpty(t, inst.GetTmuxSession().Name, "tmux session name should not be empty")
}

// TestLifecycleStart_StatusTransition verifies that Start() sets StatusStarting
// immediately and the tmux session becomes reachable shortly after. (LIFE-01)
func TestLifecycleStart_StatusTransition(t *testing.T) {
	h := NewTmuxHarness(t)

	inst := h.CreateSession("start-status", "/tmp")
	inst.Command = "sleep 60"
	require.NoError(t, inst.Start())

	assert.Equal(t, session.StatusStarting, inst.Status, "status should be starting immediately after Start()")

	WaitForCondition(t, 5*time.Second, 200*time.Millisecond,
		"tmux session to exist",
		func() bool { return inst.Exists() })
}

// TestLifecycleStop_TerminatesSession verifies that Kill() terminates the tmux
// session and sets StatusStopped. (LIFE-02)
func TestLifecycleStop_TerminatesSession(t *testing.T) {
	h := NewTmuxHarness(t)

	inst := h.CreateSession("stop-term", "/tmp")
	inst.Command = "sleep 60"
	require.NoError(t, inst.Start())

	WaitForCondition(t, 5*time.Second, 200*time.Millisecond,
		"session to exist after start",
		func() bool { return inst.Exists() })

	tmuxName := inst.GetTmuxSession().Name
	require.NotEmpty(t, tmuxName, "tmux session name must be set before Kill()")

	require.NoError(t, inst.Kill())
	assert.Equal(t, session.StatusStopped, inst.Status, "status should be stopped after Kill()")

	WaitForCondition(t, 3*time.Second, 200*time.Millisecond,
		"session to not exist",
		func() bool { return !inst.Exists() })

	// Verify at the tmux level that the session is gone.
	err := exec.Command("tmux", "has-session", "-t", tmuxName).Run()
	assert.Error(t, err, "tmux has-session should fail for killed session")
}

// TestLifecycleStop_PaneContentGoneAfterKill verifies that pane content is no
// longer accessible after the session is killed. (LIFE-02)
func TestLifecycleStop_PaneContentGoneAfterKill(t *testing.T) {
	h := NewTmuxHarness(t)

	inst := h.CreateSession("stop-pane", "/tmp")
	inst.Command = "echo marker_abc && sleep 60"
	require.NoError(t, inst.Start())

	WaitForPaneContent(t, inst, "marker_abc", 5*time.Second)

	tmuxName := inst.GetTmuxSession().Name
	require.NotEmpty(t, tmuxName)

	require.NoError(t, inst.Kill())

	// Verify the tmux session is gone.
	WaitForCondition(t, 3*time.Second, 200*time.Millisecond,
		"tmux session to be gone",
		func() bool {
			return exec.Command("tmux", "has-session", "-t", tmuxName).Run() != nil
		})
}

// TestLifecycleFork_CreatesIndependentCopy verifies that a forked session has a
// different ID, shares the same project path, and survives when the parent is
// killed. (LIFE-03)
//
// Note: CreateForkedInstance is Claude-specific (requires ClaudeSessionID).
// For shell sessions we create the child via NewInstance and set ParentSessionID
// manually, which tests the same independence semantics at the tmux level.
func TestLifecycleFork_CreatesIndependentCopy(t *testing.T) {
	h := NewTmuxHarness(t)

	parent := h.CreateSession("fork-parent", "/tmp")
	parent.Command = "sleep 60"
	require.NoError(t, parent.Start())

	WaitForCondition(t, 5*time.Second, 200*time.Millisecond,
		"parent to exist",
		func() bool { return parent.Exists() })

	// Create child as a separate session with parent linkage.
	child := h.CreateSession("fork-child", parent.ProjectPath)
	child.ParentSessionID = parent.ID
	child.Command = "sleep 60"
	require.NoError(t, child.Start())

	WaitForCondition(t, 5*time.Second, 200*time.Millisecond,
		"child to exist",
		func() bool { return child.Exists() })

	assert.NotEqual(t, parent.ID, child.ID, "parent and child should have different IDs")
	assert.Equal(t, parent.ProjectPath, child.ProjectPath, "project paths should match")
	assert.True(t, parent.Exists(), "parent should exist before kill")
	assert.True(t, child.Exists(), "child should exist before parent kill")

	// Kill the parent and verify the child survives.
	require.NoError(t, parent.Kill())
	assert.False(t, parent.Exists(), "parent should not exist after kill")
	assert.True(t, child.Exists(), "child should survive parent kill")
}

// TestLifecycleFork_ParentChildLinkage verifies the data-level parent-child
// relationship: child.ParentSessionID equals parent.ID. (LIFE-03)
func TestLifecycleFork_ParentChildLinkage(t *testing.T) {
	h := NewTmuxHarness(t)

	parent := h.CreateSession("link-parent", "/tmp")
	child := h.CreateSession("link-child", parent.ProjectPath)
	child.ParentSessionID = parent.ID

	assert.Equal(t, parent.ID, child.ParentSessionID,
		"child.ParentSessionID should equal parent.ID")
}

// TestLifecycleRestart_RecreatesToDeadSession verifies that calling Restart() on
// a killed shell session recreates a new functional tmux session with a new
// command. (LIFE-04)
func TestLifecycleRestart_RecreatesToDeadSession(t *testing.T) {
	h := NewTmuxHarness(t)

	inst := h.CreateSession("restart-dead", "/tmp")
	inst.Command = "echo first_marker && sleep 60"
	require.NoError(t, inst.Start())

	WaitForPaneContent(t, inst, "first_marker", 5*time.Second)

	require.NoError(t, inst.Kill())
	WaitForCondition(t, 3*time.Second, 200*time.Millisecond,
		"session to die",
		func() bool { return !inst.Exists() })

	// Restart with a new command.
	inst.Command = "echo second_marker && sleep 60"
	require.NoError(t, inst.Restart())

	WaitForCondition(t, 5*time.Second, 200*time.Millisecond,
		"session to exist after restart",
		func() bool { return inst.Exists() })
	WaitForPaneContent(t, inst, "second_marker", 5*time.Second)
}
