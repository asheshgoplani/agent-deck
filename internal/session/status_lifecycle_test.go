package session

import (
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Task 1: Status Transition Cycle Tests (TEST-01)
// =============================================================================

// TestStatusCycle_ShellSessionWithCommand verifies the full lifecycle:
// StatusStarting (after Start with command) -> running/idle (after grace period + UpdateStatus) -> StatusStopped (after Kill)
func TestStatusCycle_ShellSessionWithCommand(t *testing.T) {
	skipIfNoTmuxServer(t)

	inst := NewInstance("test-lifecycle-cmd", "/tmp")
	inst.Tool = "shell"
	inst.Command = "sleep 30"

	// Before Start: should be idle (default)
	require.Equal(t, StatusIdle, inst.Status, "before Start() status should be idle")

	err := inst.Start()
	require.NoError(t, err, "Start() should succeed")
	defer func() { _ = inst.Kill() }()

	// After Start with command: should be StatusStarting
	assert.Equal(t, StatusStarting, inst.Status, "after Start() with command, status should be starting")

	// Wait past the 1.5s grace period
	time.Sleep(2 * time.Second)

	err = inst.UpdateStatus()
	require.NoError(t, err, "UpdateStatus() should succeed")

	// After grace period, status should NOT be starting
	assert.NotEqual(t, StatusStarting, inst.Status, "after grace period, status should not be starting")
	// For a shell session running sleep, it should be idle (shell sessions map "waiting" to idle)
	assert.Equal(t, StatusIdle, inst.Status, "shell session running sleep should show as idle")

	// Kill the session
	err = inst.Kill()
	require.NoError(t, err, "Kill() should succeed")

	// After Kill: should be StatusStopped
	assert.Equal(t, StatusStopped, inst.Status, "after Kill() status should be stopped")

	// Tmux session should no longer exist
	assert.False(t, inst.Exists(), "tmux session should not exist after Kill()")
}

// TestStatusCycle_ShellSessionNoCommand verifies that a shell session without
// a command does NOT get StatusStarting from Start() (unlike sessions with commands).
// After Start(), the status remains StatusIdle. UpdateStatus() then reflects the
// actual tmux state which may be "starting" during the 2-minute startup window
// or "waiting"/"idle" depending on prompt detection.
func TestStatusCycle_ShellSessionNoCommand(t *testing.T) {
	skipIfNoTmuxServer(t)

	inst := NewInstance("test-lifecycle-nocmd", "/tmp")
	inst.Tool = "shell"
	// No command set

	err := inst.Start()
	require.NoError(t, err, "Start() should succeed")
	defer func() { _ = inst.Kill() }()

	// Key contract: Without a command, Start() does NOT set StatusStarting.
	// Status should remain StatusIdle (the default from NewInstance).
	assert.Equal(t, StatusIdle, inst.Status, "shell session without command should stay idle after Start()")

	// Wait past the 1.5s instance-level grace period
	time.Sleep(2 * time.Second)

	err = inst.UpdateStatus()
	require.NoError(t, err, "UpdateStatus() should succeed")

	// After UpdateStatus, the tmux layer takes over status detection.
	// The session exists and is valid, so it should NOT be StatusError.
	assert.NotEqual(t, StatusError, inst.Status,
		"shell session without command should not be in error state")
	// The status will be determined by tmux content analysis (starting, idle, or waiting).
	// During the 2-minute tmux startup window, "starting" is expected for new sessions
	// without detectable prompt patterns.
	t.Logf("Status after UpdateStatus(): %s (tmux-determined)", inst.Status)
}

// TestStatusCycle_KilledExternally verifies that when a tmux session is killed
// externally (not via inst.Kill()), UpdateStatus detects this and sets StatusError.
func TestStatusCycle_KilledExternally(t *testing.T) {
	skipIfNoTmuxServer(t)

	inst := NewInstance("test-lifecycle-ext-kill", "/tmp")
	inst.Command = "sleep 30"

	err := inst.Start()
	require.NoError(t, err, "Start() should succeed")
	defer func() { _ = inst.Kill() }() // Safety cleanup

	// Wait for grace period
	time.Sleep(2 * time.Second)

	// Verify session exists before external kill
	require.True(t, inst.Exists(), "session should exist before external kill")

	// Kill the tmux session externally (simulates user/system killing it)
	tmuxSess := inst.GetTmuxSession()
	require.NotNil(t, tmuxSess, "tmux session should not be nil")
	killCmd := exec.Command("tmux", "kill-session", "-t", tmuxSess.Name)
	err = killCmd.Run()
	require.NoError(t, err, "external tmux kill should succeed")

	// UpdateStatus should detect the missing session
	err = inst.UpdateStatus()
	require.NoError(t, err, "UpdateStatus() should succeed even when session is gone")

	// Should be StatusError after external kill
	assert.Equal(t, StatusError, inst.Status, "status should be error after external kill")
}

// TestHookFastPath_RunningStatus verifies that when a Claude-compatible instance
// has hookStatus="running" set via UpdateHookStatus, the next UpdateStatus()
// returns StatusRunning (via the hook fast path).
func TestHookFastPath_RunningStatus(t *testing.T) {
	skipIfNoTmuxServer(t)

	inst := NewInstanceWithTool("test-hook-running", "/tmp", "claude")
	inst.Command = "sleep 30" // Need a command so the session actually does something

	err := inst.Start()
	require.NoError(t, err, "Start() should succeed")
	defer func() { _ = inst.Kill() }()

	// Wait past grace period
	time.Sleep(2 * time.Second)

	// Set hook status to "running"
	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		SessionID: "test-session-123",
		Event:     "UserPromptSubmit",
		UpdatedAt: time.Now(),
	})

	// UpdateStatus should use hook fast path and return Running
	err = inst.UpdateStatus()
	require.NoError(t, err, "UpdateStatus() should succeed")
	assert.Equal(t, StatusRunning, inst.Status, "hook fast path should set status to running")
}

// TestHookFastPath_WaitingAcknowledged is a table-driven test verifying the
// hook fast path behavior for waiting status. When acknowledged=true, status
// should be StatusIdle. When acknowledged=false, status should be StatusWaiting.
func TestHookFastPath_WaitingAcknowledged(t *testing.T) {
	skipIfNoTmuxServer(t)

	tests := []struct {
		name         string
		acknowledged bool
		wantStatus   Status
	}{
		{
			name:         "waiting not acknowledged shows as waiting",
			acknowledged: false,
			wantStatus:   StatusWaiting,
		},
		{
			name:         "waiting acknowledged shows as idle",
			acknowledged: true,
			wantStatus:   StatusIdle,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := NewInstanceWithTool("test-hook-ack-"+tt.name, "/tmp", "claude")
			inst.Command = "sleep 30"

			err := inst.Start()
			require.NoError(t, err, "Start() should succeed")
			defer func() { _ = inst.Kill() }()

			// Wait past grace period
			time.Sleep(2 * time.Second)

			// Set acknowledged state on tmux session
			tmuxSess := inst.GetTmuxSession()
			require.NotNil(t, tmuxSess, "tmux session should not be nil")

			if tt.acknowledged {
				tmuxSess.Acknowledge()
			} else {
				tmuxSess.ResetAcknowledged()
			}

			// Set hook status to "waiting"
			inst.UpdateHookStatus(&HookStatus{
				Status:    "waiting",
				SessionID: "test-session-456",
				Event:     "Stop",
				UpdatedAt: time.Now(),
			})

			// UpdateStatus should use hook fast path
			err = inst.UpdateStatus()
			require.NoError(t, err, "UpdateStatus() should succeed")
			assert.Equal(t, tt.wantStatus, inst.Status,
				"hook fast path waiting with acknowledged=%v should be %s", tt.acknowledged, tt.wantStatus)
		})
	}
}

// TestHookFastPath_ShellIgnoresHooks verifies that shell tool sessions do NOT
// use the hook fast path. Shell sessions should always use tmux polling.
// The hook fast path condition requires IsClaudeCompatible(tool) || tool == "codex",
// and "shell" matches neither, so hook data should be ignored entirely.
func TestHookFastPath_ShellIgnoresHooks(t *testing.T) {
	skipIfNoTmuxServer(t)

	inst := NewInstance("test-shell-no-hooks", "/tmp")
	inst.Tool = "shell"
	inst.Command = "sleep 30"

	err := inst.Start()
	require.NoError(t, err, "Start() should succeed")
	defer func() { _ = inst.Kill() }()

	// Wait past instance-level grace period
	time.Sleep(2 * time.Second)

	// Set hook status to "running" (this should be ignored for shell tool)
	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		SessionID: "test-session-789",
		Event:     "UserPromptSubmit",
		UpdatedAt: time.Now(),
	})

	// UpdateStatus should use tmux polling, not hook fast path
	err = inst.UpdateStatus()
	require.NoError(t, err, "UpdateStatus() should succeed")

	// The critical assertion: shell sessions must NOT get StatusRunning from hooks.
	// If the hook fast path were used, the status would be Running.
	// Since shell bypasses hooks, the status comes from tmux polling.
	assert.NotEqual(t, StatusRunning, inst.Status,
		"shell session should NOT use hook fast path (status should not be running from hook data)")
	t.Logf("Shell session status from tmux polling: %s", inst.Status)
}

// =============================================================================
// Task 2: Status Persistence to SQLite Tests (TEST-07)
// =============================================================================

// TestStatusPersistence_RoundTrip verifies that saving an instance with a specific
// status to SQLite and loading it back preserves the status accurately.
func TestStatusPersistence_RoundTrip(t *testing.T) {
	s := newTestStorage(t)

	inst := &Instance{
		ID:          "persist-rt-1",
		Title:       "Round Trip Test",
		ProjectPath: "/tmp/test",
		GroupPath:   "test-group",
		Command:     "sleep 30",
		Tool:        "shell",
		Status:      StatusRunning,
		CreatedAt:   time.Now(),
	}

	err := s.SaveWithGroups([]*Instance{inst}, nil)
	require.NoError(t, err, "SaveWithGroups should succeed")

	loaded, _, err := s.LoadWithGroups()
	require.NoError(t, err, "LoadWithGroups should succeed")
	require.Len(t, loaded, 1, "should load 1 instance")

	assert.Equal(t, StatusRunning, loaded[0].Status,
		"loaded instance should have StatusRunning preserved from save")
	assert.Equal(t, "persist-rt-1", loaded[0].ID,
		"loaded instance should have the correct ID")
}

// TestStatusPersistence_UpdatedStatus verifies that saving an instance, changing
// its status, saving again, and loading reflects the updated status.
func TestStatusPersistence_UpdatedStatus(t *testing.T) {
	s := newTestStorage(t)

	inst := &Instance{
		ID:          "persist-upd-1",
		Title:       "Update Status Test",
		ProjectPath: "/tmp/test",
		GroupPath:   "test-group",
		Command:     "claude",
		Tool:        "claude",
		Status:      StatusRunning,
		CreatedAt:   time.Now(),
	}

	// Save with initial status
	err := s.SaveWithGroups([]*Instance{inst}, nil)
	require.NoError(t, err, "first SaveWithGroups should succeed")

	// Change status
	inst.Status = StatusWaiting

	// Save again with updated status
	err = s.SaveWithGroups([]*Instance{inst}, nil)
	require.NoError(t, err, "second SaveWithGroups should succeed")

	// Load and verify updated status
	loaded, _, err := s.LoadWithGroups()
	require.NoError(t, err, "LoadWithGroups should succeed")
	require.Len(t, loaded, 1, "should load 1 instance")

	assert.Equal(t, StatusWaiting, loaded[0].Status,
		"loaded instance should reflect the updated StatusWaiting")
}

// TestSubcommandPassthroughPersistence_ClearingSurvivesSave is the
// regression test for the Codex review follow-up on PR #1821:
// SubcommandPassthrough=false must survive a save even when the PREVIOUS
// saved row had it true. subcommand_passthrough lives in the tool_data
// extras zone (like idle_timeout_secs), and statedb's MergeToolDataExtras
// preserves an old extras key whenever the new blob doesn't set it — which
// would silently resurrect a stale `true` if WriteSubcommandPassthroughToToolData
// deleted the key on false instead of always writing it explicitly. This
// exercises the real save→edit→save→load cycle through SQLite, not just
// the in-memory helper functions.
func TestSubcommandPassthroughPersistence_ClearingSurvivesSave(t *testing.T) {
	s := newTestStorage(t)

	inst := &Instance{
		ID:                    "passthrough-clear-1",
		Title:                 "Passthrough Clear Test",
		ProjectPath:           "/tmp/test",
		GroupPath:             "test-group",
		Command:               "claude mcp list",
		Tool:                  "shell",
		Status:                StatusRunning,
		CreatedAt:             time.Now(),
		SubcommandPassthrough: true,
	}

	require.NoError(t, s.SaveWithGroups([]*Instance{inst}, nil), "first save should succeed")

	loaded, _, err := s.LoadWithGroups()
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	assert.True(t, loaded[0].SubcommandPassthrough, "first load should reflect SubcommandPassthrough=true")

	// Simulate SetField(FieldCommand, ...) clearing the flag on a direct
	// command edit, then re-saving — the scenario the mutator fix covers.
	inst.Command = `claude "review this repo"`
	inst.SubcommandPassthrough = false
	require.NoError(t, s.SaveWithGroups([]*Instance{inst}, nil), "second save (clearing the flag) should succeed")

	loaded2, _, err := s.LoadWithGroups()
	require.NoError(t, err)
	require.Len(t, loaded2, 1)
	assert.False(t, loaded2[0].SubcommandPassthrough,
		"SubcommandPassthrough must stay false after being explicitly cleared and re-saved — "+
			"a stale true from the previous save must not be resurrected by the tool_data extras merge")
}

// TestStatusPersistence_MultipleInstances verifies that multiple instances with
// different statuses all persist and load correctly.
func TestStatusPersistence_MultipleInstances(t *testing.T) {
	s := newTestStorage(t)

	instances := []*Instance{
		{
			ID:          "persist-multi-1",
			Title:       "Running Session",
			ProjectPath: "/tmp/test1",
			GroupPath:   "test-group",
			Command:     "claude",
			Tool:        "claude",
			Status:      StatusRunning,
			CreatedAt:   time.Now(),
		},
		{
			ID:          "persist-multi-2",
			Title:       "Waiting Session",
			ProjectPath: "/tmp/test2",
			GroupPath:   "test-group",
			Command:     "gemini",
			Tool:        "gemini",
			Status:      StatusWaiting,
			CreatedAt:   time.Now(),
		},
		{
			ID:          "persist-multi-3",
			Title:       "Idle Session",
			ProjectPath: "/tmp/test3",
			GroupPath:   "test-group",
			Command:     "sleep 30",
			Tool:        "shell",
			Status:      StatusIdle,
			CreatedAt:   time.Now(),
		},
	}

	err := s.SaveWithGroups(instances, nil)
	require.NoError(t, err, "SaveWithGroups should succeed")

	loaded, _, err := s.LoadWithGroups()
	require.NoError(t, err, "LoadWithGroups should succeed")
	require.Len(t, loaded, 3, "should load 3 instances")

	// Build lookup map by ID for order-independent comparison
	byID := make(map[string]*Instance, len(loaded))
	for _, inst := range loaded {
		byID[inst.ID] = inst
	}

	assert.Equal(t, StatusRunning, byID["persist-multi-1"].Status,
		"instance 1 should be running")
	assert.Equal(t, StatusWaiting, byID["persist-multi-2"].Status,
		"instance 2 should be waiting")
	assert.Equal(t, StatusIdle, byID["persist-multi-3"].Status,
		"instance 3 should be idle")
}

// TestStatusPersistence_EndToEnd is a full integration test that creates a real
// tmux session, gets its status via UpdateStatus(), saves to SQLite, and verifies
// the loaded status matches. Then kills the session, saves again, and verifies
// StatusStopped is persisted.
func TestStatusPersistence_EndToEnd(t *testing.T) {
	skipIfNoTmuxServer(t)

	inst := NewInstance("test-persist-e2e", "/tmp")
	inst.Tool = "shell"
	inst.Command = "sleep 30"

	err := inst.Start()
	require.NoError(t, err, "Start() should succeed")
	defer func() { _ = inst.Kill() }()

	// Wait past grace period
	time.Sleep(2 * time.Second)

	// Get actual tmux-based status
	err = inst.UpdateStatus()
	require.NoError(t, err, "UpdateStatus() should succeed")
	liveStatus := inst.Status
	t.Logf("Live status from tmux: %s", liveStatus)

	// Save to SQLite
	s := newTestStorage(t)
	err = s.SaveWithGroups([]*Instance{inst}, nil)
	require.NoError(t, err, "SaveWithGroups should succeed")

	// Load and verify status matches live status
	loaded, _, err := s.LoadWithGroups()
	require.NoError(t, err, "LoadWithGroups should succeed")
	require.Len(t, loaded, 1, "should load 1 instance")

	assert.Equal(t, liveStatus, loaded[0].Status,
		"loaded status should match the live tmux status")

	// Now kill the session
	err = inst.Kill()
	require.NoError(t, err, "Kill() should succeed")
	assert.Equal(t, StatusStopped, inst.Status, "status should be stopped after Kill()")

	// Save again with stopped status
	err = s.SaveWithGroups([]*Instance{inst}, nil)
	require.NoError(t, err, "SaveWithGroups after Kill should succeed")

	// Load and verify stopped status is persisted
	loaded2, _, err := s.LoadWithGroups()
	require.NoError(t, err, "LoadWithGroups after Kill should succeed")
	require.Len(t, loaded2, 1, "should load 1 instance after Kill")

	assert.Equal(t, StatusStopped, loaded2[0].Status,
		"loaded status after Kill should be StatusStopped")
}
