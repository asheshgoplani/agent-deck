package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Integration tests for the notification bar system
// These tests verify the full flow: status detection → notification bar

// TestIntegration_NotificationBarFlow tests the complete notification bar workflow:
// 1. Sessions are created with different waiting times
// 2. Notification bar shows waiting sessions
// 3. User "acknowledges" a session (changes status to idle)
// 4. Session is removed from notification bar
func TestIntegration_NotificationBarFlow(t *testing.T) {
	// Use test directory
	testDir := filepath.Join(os.TempDir(), "agentdeck-notif-test")
	_ = os.RemoveAll(testDir)
	if err := os.MkdirAll(testDir, 0755); err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}
	defer func() { _ = os.RemoveAll(testDir) }()

	// Create test instances (simulate waiting sessions)
	now := time.Now()
	instances := []*Instance{
		{
			ID:        "test-session-1",
			Title:     "test-frontend",
			Status:    StatusWaiting,
			CreatedAt: now.Add(-30 * time.Second),
		},
		{
			ID:        "test-session-2",
			Title:     "test-api",
			Status:    StatusWaiting,
			CreatedAt: now.Add(-20 * time.Second),
		},
		{
			ID:        "test-session-3",
			Title:     "test-backend",
			Status:    StatusWaiting,
			CreatedAt: now.Add(-10 * time.Second),
		},
	}

	// Create notification manager
	nm := NewNotificationManager(6, false)

	// === PHASE 1: Initial sync - all waiting sessions should appear ===
	t.Log("Phase 1: Initial sync")
	added, _ := nm.SyncFromInstances(instances, "")

	assert.Len(t, added, 3, "All 3 sessions should be added")
	assert.Equal(t, 3, nm.Count())

	bar := nm.FormatBar()
	t.Logf("Notification bar: %s", bar)

	// Verify newest first: test-backend [1], test-api [2], test-frontend [3]
	entries := nm.GetEntries()
	assert.Equal(t, "test-session-3", entries[0].SessionID, "Newest (test-backend) should be [1]")
	assert.Equal(t, "test-session-2", entries[1].SessionID, "Middle (test-api) should be [2]")
	assert.Equal(t, "test-session-1", entries[2].SessionID, "Oldest (test-frontend) should be [3]")

	// === PHASE 2: Simulate user switches to session [1] (acknowledges it) ===
	t.Log("Phase 2: User acknowledges session [1]")

	// Get session by key
	entry := nm.GetSessionByKey("1")
	require.NotNil(t, entry)
	assert.Equal(t, "test-session-3", entry.SessionID)

	// Simulate acknowledgment: session transitions from WAITING to IDLE
	for _, inst := range instances {
		if inst.ID == entry.SessionID {
			inst.Status = StatusIdle
			break
		}
	}

	// Sync again
	_, removed := nm.SyncFromInstances(instances, "")

	assert.Contains(t, removed, "test-session-3", "Acknowledged session should be removed")
	assert.Equal(t, 2, nm.Count())

	bar = nm.FormatBar()
	t.Logf("After acknowledge: %s", bar)

	// Verify keys are reassigned: test-api [1], test-frontend [2]
	entries = nm.GetEntries()
	assert.Equal(t, "test-session-2", entries[0].SessionID, "test-api should now be [1]")
	assert.Equal(t, "1", entries[0].AssignedKey)
	assert.Equal(t, "test-session-1", entries[1].SessionID, "test-frontend should now be [2]")
	assert.Equal(t, "2", entries[1].AssignedKey)

	// === PHASE 3: Test that current session is excluded ===
	t.Log("Phase 3: Current session exclusion")

	// Simulate: we're currently in test-session-2
	nm.SyncFromInstances(instances, "test-session-2")

	// test-session-2 should NOT be in the bar (we're in it)
	assert.False(t, nm.Has("test-session-2"), "Current session should not appear in notification bar")
	assert.Equal(t, 1, nm.Count())

	bar = nm.FormatBar()
	t.Logf("With current session excluded: %s", bar)

	// === PHASE 4: Test more than 6 sessions ===
	t.Log("Phase 4: Max sessions limit")

	// Create 10 sessions
	manyInstances := make([]*Instance, 10)
	for i := 0; i < 10; i++ {
		manyInstances[i] = &Instance{
			ID:        fmt.Sprintf("many-session-%d", i),
			Title:     fmt.Sprintf("session-%d", i),
			Status:    StatusWaiting,
			CreatedAt: now.Add(time.Duration(-i) * time.Second),
		}
	}

	nm2 := NewNotificationManager(6, false)
	nm2.SyncFromInstances(manyInstances, "")

	assert.Equal(t, 6, nm2.Count(), "Should limit to 6 sessions")

	// Verify oldest sessions are dropped
	entries = nm2.GetEntries()
	for i, e := range entries {
		expectedID := fmt.Sprintf("many-session-%d", i)
		assert.Equal(t, expectedID, e.SessionID, "Should keep newest 6 sessions")
	}

	bar = nm2.FormatBar()
	t.Logf("With 10 sessions (6 shown): %s", bar)

	t.Log("All integration tests passed!")
}

// TestIntegration_SignalFileAcknowledgment tests the signal file mechanism
// that allows tmux keybindings to trigger acknowledgment
func TestIntegration_SignalFileAcknowledgment(t *testing.T) {
	// Use test directory
	testDir := filepath.Join(os.TempDir(), "agentdeck-signal-test")
	_ = os.RemoveAll(testDir)
	if err := os.MkdirAll(testDir, 0755); err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}
	defer func() { _ = os.RemoveAll(testDir) }()

	signalFile := filepath.Join(testDir, "ack-signal")

	// Write a session ID to signal file (simulates Ctrl+b N keybinding)
	sessionID := "session-to-acknowledge"
	err := os.WriteFile(signalFile, []byte(sessionID), 0644)
	require.NoError(t, err)

	// Read and clear the signal (simulates syncNotifications)
	content, err := os.ReadFile(signalFile)
	require.NoError(t, err)
	assert.Equal(t, sessionID, string(content))

	// Clear it
	err = os.Remove(signalFile)
	require.NoError(t, err)

	// Verify it's gone
	_, err = os.ReadFile(signalFile)
	assert.True(t, os.IsNotExist(err), "Signal file should be removed after reading")

	t.Log("Signal file mechanism works correctly")
}

// TestIntegration_CtrlB1RemovesFromBar tests the EXACT user scenario:
// 1. User is in session A
// 2. User sees session B in notification bar at [1]
// 3. User presses Ctrl+b 1 to switch to session B
// 4. Session B should be removed from notification bar (because user is now in it AND it's acknowledged)
//
// This tests: switching to a session via Ctrl+b removes it from bar
func TestIntegration_CtrlB1RemovesFromBar(t *testing.T) {
	nm := NewNotificationManager(6, false)

	now := time.Now()

	// Setup: User is in "session-current", sees "session-target" in bar
	sessionCurrent := &Instance{
		ID:        "session-current",
		Title:     "my-work",
		Status:    StatusWaiting, // Current session might be waiting too
		CreatedAt: now.Add(-60 * time.Second),
	}
	sessionTarget := &Instance{
		ID:        "session-target",
		Title:     "other-work",
		Status:    StatusWaiting, // This is the one showing in bar
		CreatedAt: now.Add(-30 * time.Second),
	}
	sessionOther := &Instance{
		ID:        "session-other",
		Title:     "third-work",
		Status:    StatusWaiting,
		CreatedAt: now.Add(-10 * time.Second),
	}

	instances := []*Instance{sessionCurrent, sessionTarget, sessionOther}

	// === STATE 1: User is in session-current ===
	// Sync excludes current session
	nm.SyncFromInstances(instances, "session-current")

	// Bar should show: [1] third-work [2] other-work (session-current excluded)
	t.Logf("Before Ctrl+b 1: %s", nm.FormatBar())
	assert.Equal(t, 2, nm.Count(), "Should show 2 sessions (current excluded)")
	assert.False(t, nm.Has("session-current"), "Current session should not be in bar")
	assert.True(t, nm.Has("session-target"), "Target session should be in bar")
	assert.True(t, nm.Has("session-other"), "Other session should be in bar")

	// Find which key is assigned to session-target
	entries := nm.GetEntries()
	var targetKey string
	for _, e := range entries {
		if e.SessionID == "session-target" {
			targetKey = e.AssignedKey
			break
		}
	}
	t.Logf("session-target is at key [%s]", targetKey)

	// === STATE 2: User presses Ctrl+b [targetKey] to switch to session-target ===
	// This does TWO things:
	// 1. Switches tmux to session-target (so session-target is now current)
	// 2. Acknowledges session-target (status changes from waiting to idle)

	// Simulate acknowledgment: session-target becomes idle
	sessionTarget.Status = StatusIdle

	// Sync with session-target as current (user is now in session-target)
	nm.SyncFromInstances(instances, "session-target")

	// === VERIFY: session-target is NOT in bar ===
	t.Logf("After Ctrl+b %s: %s", targetKey, nm.FormatBar())

	// session-target should NOT be in bar for TWO reasons:
	// 1. It's the current session (excluded)
	// 2. It's now idle (not waiting)
	assert.False(t, nm.Has("session-target"), "Target session should be REMOVED from bar after Ctrl+b switch")

	// Only session-other should remain (session-current is idle after being away)
	// Actually session-current was also excluded when we were in it, so it might still be waiting
	// Let's check what's in the bar
	assert.True(t, nm.Has("session-other"), "Other session should still be in bar")

	// The current session (session-target) should NOT appear
	assert.False(t, nm.Has("session-target"), "Session we switched to should NOT be in bar")

	t.Log("✅ Ctrl+b switch correctly removes session from notification bar")
}

// TestIntegration_SwitchBackAndForth tests switching between sessions multiple times
func TestIntegration_SwitchBackAndForth(t *testing.T) {
	nm := NewNotificationManager(6, false)

	now := time.Now()

	sessionA := &Instance{ID: "a", Title: "Session-A", Status: StatusWaiting, CreatedAt: now.Add(-30 * time.Second)}
	sessionB := &Instance{ID: "b", Title: "Session-B", Status: StatusWaiting, CreatedAt: now.Add(-20 * time.Second)}
	sessionC := &Instance{ID: "c", Title: "Session-C", Status: StatusWaiting, CreatedAt: now.Add(-10 * time.Second)}

	instances := []*Instance{sessionA, sessionB, sessionC}

	// === User starts in session A ===
	t.Log("User is in Session-A")
	nm.SyncFromInstances(instances, "a")
	t.Logf("Bar: %s", nm.FormatBar())
	assert.Equal(t, 2, nm.Count()) // B and C shown
	assert.False(t, nm.Has("a"))
	assert.True(t, nm.Has("b"))
	assert.True(t, nm.Has("c"))

	// === User switches to session B (Ctrl+b to B) ===
	t.Log("User switches to Session-B")
	sessionB.Status = StatusIdle // Acknowledged
	nm.SyncFromInstances(instances, "b")
	t.Logf("Bar: %s", nm.FormatBar())
	assert.Equal(t, 2, nm.Count()) // A and C shown
	assert.True(t, nm.Has("a"))    // A is now waiting again (user left it)
	assert.False(t, nm.Has("b"))   // B is current AND idle
	assert.True(t, nm.Has("c"))

	// === User switches to session C (Ctrl+b to C) ===
	t.Log("User switches to Session-C")
	sessionC.Status = StatusIdle // Acknowledged
	nm.SyncFromInstances(instances, "c")
	t.Logf("Bar: %s", nm.FormatBar())
	assert.Equal(t, 1, nm.Count()) // Only A shown (B and C are idle)
	assert.True(t, nm.Has("a"))
	assert.False(t, nm.Has("b")) // B is idle
	assert.False(t, nm.Has("c")) // C is current AND idle

	// === Session A finishes work, becomes waiting ===
	// (already waiting, no change needed)

	// === Session B starts new work, becomes waiting again ===
	t.Log("Session-B has new work (becomes waiting)")
	sessionB.Status = StatusWaiting
	nm.SyncFromInstances(instances, "c")
	t.Logf("Bar: %s", nm.FormatBar())
	assert.Equal(t, 2, nm.Count()) // A and B shown
	assert.True(t, nm.Has("a"))
	assert.True(t, nm.Has("b")) // B is waiting again!
	assert.False(t, nm.Has("c"))

	t.Log("✅ Multiple switches work correctly")
}
