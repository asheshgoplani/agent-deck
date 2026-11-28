package session

import (
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Task 1: Session start and stop tests (TEST-03, TEST-04) ---

// TestSessionStart_CreatesTmuxSession verifies that Start() creates a real tmux
// session that is detectable both via Exists() and raw `tmux has-session`.
func TestSessionStart_CreatesTmuxSession(t *testing.T) {
	skipIfNoTmuxServer(t)

	inst := NewInstance("test-start-creates", "/tmp")
	inst.Command = "sleep 60"

	err := inst.Start()
	require.NoError(t, err, "Start() should succeed")
	defer func() { _ = inst.Kill() }()

	// Verify via Instance.Exists()
	assert.True(t, inst.Exists(), "Exists() should return true after Start()")

	// Verify tmux session object is available
	tmuxSess := inst.GetTmuxSession()
	require.NotNil(t, tmuxSess, "GetTmuxSession() should not be nil after Start()")
	assert.NotEmpty(t, tmuxSess.Name, "tmux session name should be non-empty")

	// Verify via raw tmux has-session command (independent verification)
	err = exec.Command("tmux", "has-session", "-t", tmuxSess.Name).Run()
	assert.NoError(t, err, "tmux has-session should succeed for the started session")
}

// TestSessionStart_SetsStartingStatus verifies that Start() sets the status to
// StatusStarting when a command is provided.
func TestSessionStart_SetsStartingStatus(t *testing.T) {
	skipIfNoTmuxServer(t)

	inst := NewInstance("test-start-status", "/tmp")
	inst.Command = "sleep 60"

	err := inst.Start()
	require.NoError(t, err, "Start() should succeed")
	defer func() { _ = inst.Kill() }()

	// Immediately after Start(), status should be StatusStarting (before grace period)
	assert.Equal(t, StatusStarting, inst.Status,
		"Status should be StatusStarting immediately after Start() with a command")
}

// TestSessionStop_KillsAndSetsStopped verifies that Kill() terminates the tmux
// session and sets Status to StatusStopped.
func TestSessionStop_KillsAndSetsStopped(t *testing.T) {
	skipIfNoTmuxServer(t)

	inst := NewInstance("test-stop-kills", "/tmp")
	inst.Command = "sleep 60"

	err := inst.Start()
	require.NoError(t, err, "Start() should succeed")

	// Verify session exists before kill
	tmuxName := inst.GetTmuxSession().Name
	require.True(t, inst.Exists(), "session should exist before Kill()")

	err = inst.Kill()
	require.NoError(t, err, "Kill() should succeed")

	// Verify status is stopped
	assert.Equal(t, StatusStopped, inst.Status,
		"Status should be StatusStopped after Kill()")

	// Verify Exists() returns false
	exists, probeErr := inst.GetTmuxSession().ProbeExists()
	require.NoError(t, probeErr)
	assert.False(t, exists, "Exists() should return false after Kill()")

	// Verify via raw tmux has-session (session should be gone)
	err = exec.Command("tmux", "has-session", "-t", tmuxName).Run()
	assert.Error(t, err, "tmux has-session should fail after Kill()")
}

// TestSessionStop_DoubleKill verifies that calling Kill() twice does not panic.
// The second call may return an error (tmux session already gone), which is acceptable.
func TestSessionStop_DoubleKill(t *testing.T) {
	skipIfNoTmuxServer(t)

	inst := NewInstance("test-stop-double", "/tmp")
	inst.Command = "sleep 60"

	err := inst.Start()
	require.NoError(t, err, "Start() should succeed")

	// First kill
	err = inst.Kill()
	require.NoError(t, err, "First Kill() should succeed")

	// Second kill should not panic (error is acceptable)
	assert.NotPanics(t, func() {
		_ = inst.Kill()
	}, "Second Kill() should not panic")
}

// TestSessionStop_UpdateStatusAfterKill verifies that UpdateStatus() preserves
// StatusStopped after the session has been intentionally killed.
func TestSessionStop_UpdateStatusAfterKill(t *testing.T) {
	skipIfNoTmuxServer(t)

	inst := NewInstance("test-stop-update", "/tmp")
	inst.Command = "sleep 60"

	err := inst.Start()
	require.NoError(t, err, "Start() should succeed")

	err = inst.Kill()
	require.NoError(t, err, "Kill() should succeed")

	// Wait past any grace period (1.5s) so UpdateStatus does a real check
	time.Sleep(2 * time.Second)

	err = inst.UpdateStatus()
	require.NoError(t, err, "UpdateStatus() should not error")

	assert.Equal(t, StatusStopped, inst.Status,
		"UpdateStatus() should preserve StatusStopped after Kill()")
}

// TestSessionStart_NilTmuxSession verifies that Start() on a bare Instance
// without tmux initialization returns an appropriate error.
func TestSessionStart_NilTmuxSession(t *testing.T) {
	// Create a bare instance without tmux session (no NewInstance)
	inst := &Instance{}

	err := inst.Start()
	require.Error(t, err, "Start() should fail without tmux session")
	assert.Contains(t, err.Error(), "tmux session not initialized",
		"error should mention tmux session not initialized")
}

// --- Task 2: Session fork and attach tests (TEST-05, TEST-06) ---

// TestSessionFork_CreatesForkWithDifferentID verifies that CreateForkedInstance
// produces a new Instance with a different ID but the same ProjectPath and Tool.
func TestSessionFork_CreatesForkWithDifferentID(t *testing.T) {
	inst := NewInstanceWithTool("test-fork-parent", "/tmp", "claude")
	inst.ClaudeSessionID = "test-session-123"
	inst.ClaudeDetectedAt = time.Now()

	forked, _, err := inst.CreateForkedInstance("forked-test", "")
	require.NoError(t, err, "CreateForkedInstance should succeed")

	// Forked instance should have a different ID
	assert.NotEqual(t, inst.ID, forked.ID,
		"forked instance should have a different ID from parent")

	// Forked instance should inherit ProjectPath
	assert.Equal(t, inst.ProjectPath, forked.ProjectPath,
		"forked instance should have the same ProjectPath")

	// Forked instance should have Tool set to claude
	assert.Equal(t, "claude", forked.Tool,
		"forked instance should have Tool set to claude")
}

// TestSessionFork_IndependentTmuxSession verifies that two sessions have
// independent tmux sessions: killing one does not affect the other.
func TestSessionFork_IndependentTmuxSession(t *testing.T) {
	skipIfNoTmuxServer(t)

	// Create parent session (shell tool, not claude, to avoid fork command complexity)
	parent := NewInstance("test-fork-parent-tmux", "/tmp")
	parent.Command = "sleep 60"

	err := parent.Start()
	require.NoError(t, err, "parent Start() should succeed")
	defer func() { _ = parent.Kill() }()

	// Create child session independently
	child := NewInstance("test-fork-child-tmux", "/tmp")
	child.Command = "sleep 60"

	err = child.Start()
	require.NoError(t, err, "child Start() should succeed")
	defer func() { _ = child.Kill() }()

	// Both should exist
	assert.True(t, parent.Exists(), "parent should exist")
	assert.True(t, child.Exists(), "child should exist")

	// They should have different tmux session names
	assert.NotEqual(t, parent.GetTmuxSession().Name, child.GetTmuxSession().Name,
		"parent and child should have different tmux session names")

	// Kill parent and verify child survives (independence)
	err = parent.Kill()
	require.NoError(t, err, "parent Kill() should succeed")

	exists, probeErr := parent.GetTmuxSession().ProbeExists()
	require.NoError(t, probeErr)
	assert.False(t, exists, "parent should not exist after Kill()")
	assert.True(t, child.Exists(), "child should still exist after parent Kill()")
}

// TestSessionFork_CanForkStaleness verifies the CanFork() staleness threshold.
// A session with a ClaudeSessionID detected more than 5 minutes ago cannot fork.
func TestSessionFork_CanForkStaleness(t *testing.T) {
	tests := []struct {
		name       string
		sessionID  string
		detectedAt time.Time
		tool       string
		wantFork   bool
	}{
		{
			name:       "no session ID",
			sessionID:  "",
			detectedAt: time.Now(),
			tool:       "claude",
			wantFork:   false,
		},
		{
			name:       "recent detection (within threshold)",
			sessionID:  "abc-123",
			detectedAt: time.Now(),
			tool:       "claude",
			wantFork:   true,
		},
		{
			name:       "stale detection (4 minutes, still within 5min threshold)",
			sessionID:  "abc-123",
			detectedAt: time.Now().Add(-4 * time.Minute),
			tool:       "claude",
			wantFork:   true,
		},
		{
			name:       "stale detection (6 minutes, beyond 5min threshold)",
			sessionID:  "abc-123",
			detectedAt: time.Now().Add(-6 * time.Minute),
			tool:       "claude",
			wantFork:   false,
		},
		{
			name:       "stale detection (10 minutes, well beyond threshold)",
			sessionID:  "abc-123",
			detectedAt: time.Now().Add(-10 * time.Minute),
			tool:       "claude",
			wantFork:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := NewInstanceWithTool("test-canfork", "/tmp", tt.tool)
			inst.ClaudeSessionID = tt.sessionID
			inst.ClaudeDetectedAt = tt.detectedAt

			got := inst.CanFork()
			assert.Equal(t, tt.wantFork, got,
				"CanFork() for %s", tt.name)
		})
	}
}

// TestSessionAttach_Preconditions verifies that an un-started instance has
// a nil tmux session, making attach impossible.
func TestSessionAttach_Preconditions(t *testing.T) {
	// Un-started instance created via NewInstance has a tmuxSession allocated
	// but not yet started. However, a bare Instance{} has nil tmuxSession.
	bare := &Instance{}
	assert.Nil(t, bare.GetTmuxSession(),
		"bare Instance should have nil tmux session (attach impossible)")

	// Also verify that Exists() returns false for un-started instances
	inst := NewInstance("test-attach-precond", "/tmp")
	assert.False(t, inst.Exists(),
		"un-started instance should not exist in tmux")
}

// TestSessionAttach_RunningSessionHasTmuxSession verifies that a running session
// has a non-nil, existing tmux session, satisfying the attach precondition.
// Full attach test requires PTY; we verify the precondition that a running session
// has an attachable tmux session.
func TestSessionAttach_RunningSessionHasTmuxSession(t *testing.T) {
	skipIfNoTmuxServer(t)

	inst := NewInstance("test-attach-running", "/tmp")
	inst.Command = "sleep 60"

	err := inst.Start()
	require.NoError(t, err, "Start() should succeed")
	defer func() { _ = inst.Kill() }()

	tmuxSess := inst.GetTmuxSession()
	require.NotNil(t, tmuxSess,
		"running session should have a non-nil tmux session")
	assert.True(t, tmuxSess.Exists(),
		"running session's tmux session should exist (attach precondition satisfied)")
}
