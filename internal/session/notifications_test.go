package session

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNotificationManager_Add(t *testing.T) {
	nm := NewNotificationManager(6, false)

	inst := &Instance{
		ID:     "abc123",
		Title:  "frontend",
		Status: StatusWaiting,
	}

	err := nm.Add(inst)
	require.NoError(t, err)

	entries := nm.GetEntries()
	assert.Len(t, entries, 1)
	assert.Equal(t, "frontend", entries[0].Title)
	assert.Equal(t, "1", entries[0].AssignedKey)
}

func TestNotificationManager_NewestFirst(t *testing.T) {
	nm := NewNotificationManager(6, false)

	// Add three sessions with delays
	inst1 := &Instance{ID: "a", Title: "first", Status: StatusWaiting}
	if err := nm.Add(inst1); err != nil {
		t.Fatalf("failed to add inst1: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	inst2 := &Instance{ID: "b", Title: "second", Status: StatusWaiting}
	if err := nm.Add(inst2); err != nil {
		t.Fatalf("failed to add inst2: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	inst3 := &Instance{ID: "c", Title: "third", Status: StatusWaiting}
	if err := nm.Add(inst3); err != nil {
		t.Fatalf("failed to add inst3: %v", err)
	}

	entries := nm.GetEntries()
	assert.Len(t, entries, 3)
	// Newest should be at position [0] with key "1"
	assert.Equal(t, "third", entries[0].Title)
	assert.Equal(t, "1", entries[0].AssignedKey)
	assert.Equal(t, "second", entries[1].Title)
	assert.Equal(t, "2", entries[1].AssignedKey)
	assert.Equal(t, "first", entries[2].Title)
	assert.Equal(t, "3", entries[2].AssignedKey)
}

func TestNotificationManager_Remove(t *testing.T) {
	nm := NewNotificationManager(6, false)

	inst1 := &Instance{ID: "a", Title: "first", Status: StatusWaiting}
	inst2 := &Instance{ID: "b", Title: "second", Status: StatusWaiting}
	_ = nm.Add(inst1)
	_ = nm.Add(inst2)

	nm.Remove("b") // Remove newest

	entries := nm.GetEntries()
	assert.Len(t, entries, 1)
	assert.Equal(t, "first", entries[0].Title)
	assert.Equal(t, "1", entries[0].AssignedKey) // Should shift to key "1"
}

func TestNotificationManager_MaxShown(t *testing.T) {
	nm := NewNotificationManager(3, false) // Max 3

	for i := 0; i < 5; i++ {
		inst := &Instance{ID: string(rune('a' + i)), Title: string(rune('A' + i)), Status: StatusWaiting}
		_ = nm.Add(inst)
		time.Sleep(5 * time.Millisecond)
	}

	entries := nm.GetEntries()
	assert.Len(t, entries, 3) // Only 3 shown
	// Newest 3 should be shown
	assert.Equal(t, "E", entries[0].Title) // newest
	assert.Equal(t, "D", entries[1].Title)
	assert.Equal(t, "C", entries[2].Title)
}

func TestNotificationManager_FormatBar(t *testing.T) {
	nm := NewNotificationManager(6, false)

	// Empty bar
	assert.Equal(t, "", nm.FormatBar())

	// One session
	_ = nm.Add(&Instance{ID: "a", Title: "frontend", Status: StatusWaiting})
	bar := nm.FormatBar()
	assert.Contains(t, bar, "[1]")
	assert.Contains(t, bar, "frontend")

	// Two sessions
	_ = nm.Add(&Instance{ID: "b", Title: "api", Status: StatusWaiting})
	bar = nm.FormatBar()
	assert.Contains(t, bar, "[1]")
	assert.Contains(t, bar, "[2]")
}

func TestNotificationManager_FullTitles(t *testing.T) {
	nm := NewNotificationManager(6, false)

	// Add 6 sessions with long names
	for i := 0; i < 6; i++ {
		inst := &Instance{
			ID:     string(rune('a' + i)),
			Title:  "verylongsessionname" + string(rune('0'+i)),
			Status: StatusWaiting,
		}
		_ = nm.Add(inst)
	}

	bar := nm.FormatBar()
	// Full titles should be shown (no truncation)
	// Each title is ~20 chars, bar should contain all of them
	assert.Contains(t, bar, "verylongsessionname5") // newest
	assert.Contains(t, bar, "verylongsessionname0") // oldest
}

func TestNotificationManager_GetSessionByKey(t *testing.T) {
	nm := NewNotificationManager(6, false)

	inst1 := &Instance{ID: "a", Title: "first", Status: StatusWaiting}
	inst2 := &Instance{ID: "b", Title: "second", Status: StatusWaiting}
	_ = nm.Add(inst1)
	_ = nm.Add(inst2)

	// Key "1" should return newest (second)
	entry := nm.GetSessionByKey("1")
	require.NotNil(t, entry)
	assert.Equal(t, "b", entry.SessionID)

	// Key "2" should return first
	entry = nm.GetSessionByKey("2")
	require.NotNil(t, entry)
	assert.Equal(t, "a", entry.SessionID)

	// Key "3" should return nil
	entry = nm.GetSessionByKey("3")
	assert.Nil(t, entry)
}

func TestNotificationManager_Count(t *testing.T) {
	nm := NewNotificationManager(6, false)

	assert.Equal(t, 0, nm.Count())

	_ = nm.Add(&Instance{ID: "a", Title: "first", Status: StatusWaiting})
	assert.Equal(t, 1, nm.Count())

	_ = nm.Add(&Instance{ID: "b", Title: "second", Status: StatusWaiting})
	assert.Equal(t, 2, nm.Count())

	nm.Remove("a")
	assert.Equal(t, 1, nm.Count())
}

func TestNotificationManager_Clear(t *testing.T) {
	nm := NewNotificationManager(6, false)

	_ = nm.Add(&Instance{ID: "a", Title: "first", Status: StatusWaiting})
	_ = nm.Add(&Instance{ID: "b", Title: "second", Status: StatusWaiting})
	assert.Equal(t, 2, nm.Count())

	nm.Clear()
	assert.Equal(t, 0, nm.Count())
	assert.Empty(t, nm.GetEntries())
}

func TestNotificationManager_Has(t *testing.T) {
	nm := NewNotificationManager(6, false)

	_ = nm.Add(&Instance{ID: "a", Title: "first", Status: StatusWaiting})

	assert.True(t, nm.Has("a"))
	assert.False(t, nm.Has("b"))
}

func TestNotificationManager_DuplicateAdd(t *testing.T) {
	nm := NewNotificationManager(6, false)

	inst := &Instance{ID: "a", Title: "first", Status: StatusWaiting}
	_ = nm.Add(inst)
	_ = nm.Add(inst) // Add same instance again

	// Should only have one entry
	assert.Equal(t, 1, nm.Count())
}

func TestNotificationManager_SyncFromInstances(t *testing.T) {
	nm := NewNotificationManager(6, false)

	// Initial add
	_ = nm.Add(&Instance{ID: "a", Title: "first", Status: StatusWaiting})

	instances := []*Instance{
		{ID: "a", Title: "first", Status: StatusWaiting},  // Still waiting
		{ID: "b", Title: "second", Status: StatusWaiting}, // New waiting
		{ID: "c", Title: "third", Status: StatusIdle},     // Not waiting
	}

	added, removed := nm.SyncFromInstances(instances, "")

	assert.Contains(t, added, "b")
	assert.Empty(t, removed)
	assert.Equal(t, 2, nm.Count())
	assert.True(t, nm.Has("a"))
	assert.True(t, nm.Has("b"))
	assert.False(t, nm.Has("c"))
}

func TestNotificationManager_SyncFromInstances_RemovesNonWaiting(t *testing.T) {
	nm := NewNotificationManager(6, false)

	_ = nm.Add(&Instance{ID: "a", Title: "first", Status: StatusWaiting})
	_ = nm.Add(&Instance{ID: "b", Title: "second", Status: StatusWaiting})

	// "a" is no longer waiting (became idle)
	instances := []*Instance{
		{ID: "a", Title: "first", Status: StatusIdle},
		{ID: "b", Title: "second", Status: StatusWaiting},
	}

	added, removed := nm.SyncFromInstances(instances, "")

	assert.Empty(t, added)
	assert.Contains(t, removed, "a")
	assert.Equal(t, 1, nm.Count())
	assert.False(t, nm.Has("a"))
	assert.True(t, nm.Has("b"))
}

func TestNotificationManager_SyncFromInstances_ExcludesCurrentSession(t *testing.T) {
	nm := NewNotificationManager(6, false)

	instances := []*Instance{
		{ID: "current", Title: "current-session", Status: StatusWaiting},
		{ID: "other", Title: "other-session", Status: StatusWaiting},
	}

	// Sync with "current" as the current session - it should be excluded
	added, _ := nm.SyncFromInstances(instances, "current")

	assert.Contains(t, added, "other")
	assert.NotContains(t, added, "current")
	assert.Equal(t, 1, nm.Count())
	assert.False(t, nm.Has("current"))
	assert.True(t, nm.Has("other"))
}

func TestNotificationManager_DefaultMaxShown(t *testing.T) {
	nm := NewNotificationManager(0, false) // Invalid value should default to 6

	for i := 0; i < 10; i++ {
		_ = nm.Add(&Instance{ID: string(rune('a' + i)), Title: string(rune('A' + i)), Status: StatusWaiting})
	}

	assert.Equal(t, 6, nm.Count())
}

// TestNotificationManager_SyncFromInstances_NewestFirst verifies that SyncFromInstances
// correctly sorts entries with newest waiting sessions first.
func TestNotificationManager_SyncFromInstances_NewestFirst(t *testing.T) {
	nm := NewNotificationManager(6, false)

	now := time.Now()

	// Create instances with different CreatedAt times (used as fallback for GetWaitingSince)
	instances := []*Instance{
		{ID: "oldest", Title: "oldest-session", Status: StatusWaiting, CreatedAt: now.Add(-30 * time.Second)},
		{ID: "middle", Title: "middle-session", Status: StatusWaiting, CreatedAt: now.Add(-15 * time.Second)},
		{ID: "newest", Title: "newest-session", Status: StatusWaiting, CreatedAt: now},
	}

	added, _ := nm.SyncFromInstances(instances, "")

	assert.Len(t, added, 3)
	assert.Equal(t, 3, nm.Count())

	entries := nm.GetEntries()
	// Newest should be first (key "1")
	assert.Equal(t, "newest", entries[0].SessionID)
	assert.Equal(t, "1", entries[0].AssignedKey)
	assert.Equal(t, "middle", entries[1].SessionID)
	assert.Equal(t, "2", entries[1].AssignedKey)
	assert.Equal(t, "oldest", entries[2].SessionID)
	assert.Equal(t, "3", entries[2].AssignedKey)
}

// TestNotificationManager_SyncFromInstances_MixedNewAndExisting verifies sorting
// works correctly when mixing new and existing entries
func TestNotificationManager_SyncFromInstances_MixedNewAndExisting(t *testing.T) {
	nm := NewNotificationManager(6, false)

	now := time.Now()

	// First sync: add one session
	existingSession := &Instance{
		ID: "existing", Title: "existing-session", Status: StatusWaiting,
		CreatedAt: now.Add(-60 * time.Second),
	}
	nm.SyncFromInstances([]*Instance{existingSession}, "")
	assert.Equal(t, 1, nm.Count())

	// Second sync: add new sessions (some newer, some older than existing)
	instances := []*Instance{
		existingSession,
		{ID: "newest", Title: "newest-session", Status: StatusWaiting, CreatedAt: now},
		{ID: "older", Title: "older-session", Status: StatusWaiting, CreatedAt: now.Add(-120 * time.Second)},
		{ID: "middle", Title: "middle-session", Status: StatusWaiting, CreatedAt: now.Add(-30 * time.Second)},
	}

	nm.SyncFromInstances(instances, "")

	entries := nm.GetEntries()
	assert.Len(t, entries, 4)

	// Should be sorted: newest, middle, existing, older
	assert.Equal(t, "newest", entries[0].SessionID)
	assert.Equal(t, "middle", entries[1].SessionID)
	assert.Equal(t, "existing", entries[2].SessionID)
	assert.Equal(t, "older", entries[3].SessionID)
}

// =============================================================================
// ISSUE VERIFICATION TESTS
// These tests verify the 4 notification bar issues are fixed
// =============================================================================

// TestIssue1_AcknowledgmentRemovesFromBar verifies that when a session becomes idle
// (acknowledged), it gets removed from the notification bar on next sync.
// ISSUE: Sessions not acknowledged when switching via Ctrl+b shortcuts
func TestIssue1_AcknowledgmentRemovesFromBar(t *testing.T) {
	nm := NewNotificationManager(6, false)

	now := time.Now()

	// Create 3 waiting sessions
	instances := []*Instance{
		{ID: "session1", Title: "session-1", Status: StatusWaiting, CreatedAt: now.Add(-30 * time.Second)},
		{ID: "session2", Title: "session-2", Status: StatusWaiting, CreatedAt: now.Add(-20 * time.Second)},
		{ID: "session3", Title: "session-3", Status: StatusWaiting, CreatedAt: now.Add(-10 * time.Second)},
	}

	// Initial sync - all 3 should be in bar
	nm.SyncFromInstances(instances, "")
	assert.Equal(t, 3, nm.Count(), "All 3 waiting sessions should be in notification bar")
	assert.True(t, nm.Has("session1"))
	assert.True(t, nm.Has("session2"))
	assert.True(t, nm.Has("session3"))

	// Simulate: User switches to session2 (Ctrl+b 2) and acknowledges it
	// Session2 transitions from WAITING to IDLE
	instances[1].Status = StatusIdle // session2 is now idle (acknowledged)

	// Sync again
	_, removed := nm.SyncFromInstances(instances, "")

	// session2 should be removed from bar
	assert.Contains(t, removed, "session2", "Acknowledged session should be removed from bar")
	assert.Equal(t, 2, nm.Count(), "Only 2 sessions should remain in bar")
	assert.True(t, nm.Has("session1"), "session1 should still be in bar")
	assert.False(t, nm.Has("session2"), "session2 (now idle) should NOT be in bar")
	assert.True(t, nm.Has("session3"), "session3 should still be in bar")

	// Keys should be reassigned: session3 [1], session1 [2]
	entries := nm.GetEntries()
	assert.Equal(t, "session3", entries[0].SessionID) // newest waiting
	assert.Equal(t, "1", entries[0].AssignedKey)
	assert.Equal(t, "session1", entries[1].SessionID)
	assert.Equal(t, "2", entries[1].AssignedKey)
}

// TestIssue2_MaxSixSessionsShown verifies that exactly 6 sessions are shown
// (not fewer due to bugs, not more due to config issues).
// ISSUE: Only showing ~3 sessions instead of 6
func TestIssue2_MaxSixSessionsShown(t *testing.T) {
	nm := NewNotificationManager(6, false)

	now := time.Now()

	// Create 10 waiting sessions
	instances := make([]*Instance, 10)
	for i := 0; i < 10; i++ {
		instances[i] = &Instance{
			ID:        fmt.Sprintf("session%d", i),
			Title:     fmt.Sprintf("session-%d", i),
			Status:    StatusWaiting,
			CreatedAt: now.Add(time.Duration(-i) * time.Second), // Newer sessions have smaller i
		}
	}

	nm.SyncFromInstances(instances, "")

	// Exactly 6 should be shown
	assert.Equal(t, 6, nm.Count(), "Exactly 6 sessions should be shown in notification bar")

	entries := nm.GetEntries()
	// The 6 newest should be shown (session0-5)
	for i, entry := range entries {
		expectedID := fmt.Sprintf("session%d", i)
		assert.Equal(t, expectedID, entry.SessionID, "Entry %d should be session%d", i, i)
		assert.Equal(t, fmt.Sprintf("%d", i+1), entry.AssignedKey, "Entry %d should have key %d", i, i+1)
	}

	// Verify bar format includes all 6
	bar := nm.FormatBar()
	assert.Contains(t, bar, "[1]")
	assert.Contains(t, bar, "[6]")
	assert.NotContains(t, bar, "[7]") // No 7th entry
}

// TestIssue3_NewestWaitingSessionFirst verifies that the most recently waiting
// session appears at position [1], not oldest.
// ISSUE: Newest waiting sessions should appear first at position [1]
func TestIssue3_NewestWaitingSessionFirst(t *testing.T) {
	nm := NewNotificationManager(6, false)

	now := time.Now()

	// Create sessions with different waiting times
	// Session that became waiting most recently should be [1]
	instances := []*Instance{
		{ID: "old-waiting", Title: "old-session", Status: StatusWaiting, CreatedAt: now.Add(-5 * time.Minute)},
		{ID: "mid-waiting", Title: "mid-session", Status: StatusWaiting, CreatedAt: now.Add(-2 * time.Minute)},
		{ID: "new-waiting", Title: "new-session", Status: StatusWaiting, CreatedAt: now.Add(-10 * time.Second)},
	}

	nm.SyncFromInstances(instances, "")

	entries := nm.GetEntries()
	assert.Len(t, entries, 3)

	// Newest waiting session should be [1]
	assert.Equal(t, "new-waiting", entries[0].SessionID, "Newest waiting session should be first")
	assert.Equal(t, "1", entries[0].AssignedKey, "Newest should have key 1")

	// Verify by key lookup
	entry := nm.GetSessionByKey("1")
	assert.NotNil(t, entry)
	assert.Equal(t, "new-waiting", entry.SessionID, "Key 1 should return newest waiting session")

	// Middle and oldest should follow
	assert.Equal(t, "mid-waiting", entries[1].SessionID)
	assert.Equal(t, "old-waiting", entries[2].SessionID)
}

// TestIssue4_RealTimeUpdatesAcrossSessions verifies that when one session's status
// changes, it affects the notification bar for ALL sessions on the next sync.
// ISSUE: Real-time updates across all sessions
func TestIssue4_RealTimeUpdatesAcrossSessions(t *testing.T) {
	nm := NewNotificationManager(6, false)

	now := time.Now()

	// Initial state: 2 waiting sessions
	instances := []*Instance{
		{ID: "session-a", Title: "A", Status: StatusWaiting, CreatedAt: now.Add(-30 * time.Second)},
		{ID: "session-b", Title: "B", Status: StatusWaiting, CreatedAt: now.Add(-20 * time.Second)},
		{ID: "session-c", Title: "C", Status: StatusIdle, CreatedAt: now.Add(-10 * time.Second)}, // idle
	}

	nm.SyncFromInstances(instances, "")
	assert.Equal(t, 2, nm.Count())

	bar1 := nm.FormatBar()
	t.Logf("Initial bar: %s", bar1)

	// Simulate: session-c becomes waiting (agent finished, needs attention)
	instances[2].Status = StatusWaiting // C is now waiting

	added, _ := nm.SyncFromInstances(instances, "")

	// session-c should be added
	assert.Contains(t, added, "session-c", "Newly waiting session should be added")
	assert.Equal(t, 3, nm.Count())

	bar2 := nm.FormatBar()
	t.Logf("After C becomes waiting: %s", bar2)
	assert.NotEqual(t, bar1, bar2, "Bar should change when session becomes waiting")

	// C should now be [1] since it became waiting most recently
	entries := nm.GetEntries()
	assert.Equal(t, "session-c", entries[0].SessionID, "Most recently waiting session should be first")

	// Simulate: session-b becomes idle (acknowledged)
	instances[1].Status = StatusIdle

	_, removed := nm.SyncFromInstances(instances, "")

	assert.Contains(t, removed, "session-b")
	assert.Equal(t, 2, nm.Count())

	bar3 := nm.FormatBar()
	t.Logf("After B acknowledged: %s", bar3)
	assert.NotEqual(t, bar2, bar3, "Bar should update when session becomes idle")

	// Final state: C [1], A [2]
	entries = nm.GetEntries()
	assert.Equal(t, "session-c", entries[0].SessionID)
	assert.Equal(t, "1", entries[0].AssignedKey)
	assert.Equal(t, "session-a", entries[1].SessionID)
	assert.Equal(t, "2", entries[1].AssignedKey)
}

// TestNotificationManager_KeyReassignmentAfterRemoval verifies that keys are
// correctly reassigned when sessions are removed from the middle.
func TestNotificationManager_KeyReassignmentAfterRemoval(t *testing.T) {
	nm := NewNotificationManager(6, false)

	now := time.Now()

	instances := []*Instance{
		{ID: "a", Title: "A", Status: StatusWaiting, CreatedAt: now.Add(-4 * time.Second)},
		{ID: "b", Title: "B", Status: StatusWaiting, CreatedAt: now.Add(-3 * time.Second)},
		{ID: "c", Title: "C", Status: StatusWaiting, CreatedAt: now.Add(-2 * time.Second)},
		{ID: "d", Title: "D", Status: StatusWaiting, CreatedAt: now.Add(-1 * time.Second)},
	}

	nm.SyncFromInstances(instances, "")
	entries := nm.GetEntries()
	// Order: d[1], c[2], b[3], a[4]
	assert.Equal(t, "d", entries[0].SessionID)
	assert.Equal(t, "1", entries[0].AssignedKey)
	assert.Equal(t, "a", entries[3].SessionID)
	assert.Equal(t, "4", entries[3].AssignedKey)

	// Remove C (middle session) - it becomes idle
	instances[2].Status = StatusIdle

	nm.SyncFromInstances(instances, "")
	entries = nm.GetEntries()

	// Keys should be reassigned: d[1], b[2], a[3]
	assert.Equal(t, 3, nm.Count())
	assert.Equal(t, "d", entries[0].SessionID)
	assert.Equal(t, "1", entries[0].AssignedKey)
	assert.Equal(t, "b", entries[1].SessionID)
	assert.Equal(t, "2", entries[1].AssignedKey)
	assert.Equal(t, "a", entries[2].SessionID)
	assert.Equal(t, "3", entries[2].AssignedKey)
}

// TestNotificationManager_ShowAll_DisplaysAllSessions verifies that show_all=true includes all sessions
func TestNotificationManager_ShowAll_DisplaysAllSessions(t *testing.T) {
	nm := NewNotificationManager(6, true) // show_all enabled

	now := time.Now()
	instances := []*Instance{
		{ID: "running", Title: "running-session", Status: StatusRunning, CreatedAt: now},
		{ID: "waiting", Title: "waiting-session", Status: StatusWaiting, CreatedAt: now.Add(-5 * time.Second)},
		{ID: "idle", Title: "idle-session", Status: StatusIdle, CreatedAt: now.Add(-10 * time.Second)},
		{ID: "error", Title: "error-session", Status: StatusError, CreatedAt: now.Add(-15 * time.Second)},
	}

	added, _ := nm.SyncFromInstances(instances, "")

	// All sessions should be added (not just waiting)
	assert.Len(t, added, 4)
	assert.Equal(t, 4, nm.Count())

	entries := nm.GetEntries()
	// Verify all statuses are present
	statuses := make(map[Status]bool)
	for _, e := range entries {
		statuses[e.Status] = true
	}
	assert.True(t, statuses[StatusRunning])
	assert.True(t, statuses[StatusWaiting])
	assert.True(t, statuses[StatusIdle])
	assert.True(t, statuses[StatusError])
}

// TestNotificationManager_ShowAll_ExcludesCurrentSession verifies current session is excluded in show_all mode
func TestNotificationManager_ShowAll_ExcludesCurrentSession(t *testing.T) {
	nm := NewNotificationManager(6, true) // show_all enabled

	now := time.Now()
	instances := []*Instance{
		{ID: "current", Title: "current-session", Status: StatusRunning, CreatedAt: now},
		{ID: "other", Title: "other-session", Status: StatusRunning, CreatedAt: now.Add(-5 * time.Second)},
	}

	added, _ := nm.SyncFromInstances(instances, "current")

	// Only the other session should be added
	assert.Len(t, added, 1)
	assert.Equal(t, "other", added[0])
	assert.Equal(t, 1, nm.Count())
	assert.False(t, nm.Has("current"))
	assert.True(t, nm.Has("other"))
}

// TestNotificationManager_ShowAll_StatusIcons verifies status icons appear in bar format
func TestNotificationManager_ShowAll_StatusIcons(t *testing.T) {
	nm := NewNotificationManager(6, true) // show_all enabled

	now := time.Now()
	instances := []*Instance{
		{ID: "running", Title: "running-session", Status: StatusRunning, CreatedAt: now},
		{ID: "waiting", Title: "waiting-session", Status: StatusWaiting, CreatedAt: now.Add(-5 * time.Second)},
		{ID: "idle", Title: "idle-session", Status: StatusIdle, CreatedAt: now.Add(-10 * time.Second)},
		{ID: "error", Title: "error-session", Status: StatusError, CreatedAt: now.Add(-15 * time.Second)},
	}

	nm.SyncFromInstances(instances, "")
	bar := nm.FormatBar()

	// Verify bar contains status icons
	assert.Contains(t, bar, "●") // Running
	assert.Contains(t, bar, "◐") // Waiting
	assert.Contains(t, bar, "○") // Idle
	assert.Contains(t, bar, "✕") // Error

	// Verify format: [key] icon title
	assert.Contains(t, bar, "[1] ● running-session")
	assert.Contains(t, bar, "[2] ◐ waiting-session")
	assert.Contains(t, bar, "[3] ○ idle-session")
	assert.Contains(t, bar, "[4] ✕ error-session")
}

// TestNotificationManager_DefaultMode_BackwardCompatible verifies show_all=false preserves original behavior
func TestNotificationManager_DefaultMode_BackwardCompatible(t *testing.T) {
	nm := NewNotificationManager(6, false) // Default mode

	now := time.Now()
	instances := []*Instance{
		{ID: "running", Title: "running-session", Status: StatusRunning, CreatedAt: now},
		{ID: "waiting", Title: "waiting-session", Status: StatusWaiting, CreatedAt: now.Add(-5 * time.Second)},
		{ID: "idle", Title: "idle-session", Status: StatusIdle, CreatedAt: now.Add(-10 * time.Second)},
	}

	added, _ := nm.SyncFromInstances(instances, "")

	// Only waiting session should be added (original behavior)
	assert.Len(t, added, 1)
	assert.Equal(t, "waiting", added[0])
	assert.Equal(t, 1, nm.Count())

	bar := nm.FormatBar()
	// No icons in default mode
	assert.NotContains(t, bar, "●")
	assert.NotContains(t, bar, "◐")
	assert.NotContains(t, bar, "○")
	// Original format: [key] title (no icon)
	assert.Contains(t, bar, "[1] waiting-session")
	assert.NotContains(t, bar, "[1] ◐") // Icon should not be present
}

// TestNotificationManager_ShowAll_StatusUpdates verifies status field updates on sync
func TestNotificationManager_ShowAll_StatusUpdates(t *testing.T) {
	nm := NewNotificationManager(6, true) // show_all enabled

	now := time.Now()
	inst := &Instance{ID: "session1", Title: "test-session", Status: StatusRunning, CreatedAt: now}

	nm.SyncFromInstances([]*Instance{inst}, "")
	entries := nm.GetEntries()
	assert.Equal(t, StatusRunning, entries[0].Status)

	// Change status to waiting
	inst.Status = StatusWaiting
	nm.SyncFromInstances([]*Instance{inst}, "")
	entries = nm.GetEntries()
	assert.Equal(t, StatusWaiting, entries[0].Status)

	// Verify bar format updated
	bar := nm.FormatBar()
	assert.Contains(t, bar, "◐")    // Waiting icon
	assert.NotContains(t, bar, "●") // Running icon should be gone
}
