package statedb

import (
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newTestDB(t *testing.T) *StateDB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenClose(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")

	// Open and write
	db1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db1.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := db1.SaveInstance(&InstanceRow{
		ID:          "test-1",
		Title:       "Test",
		ProjectPath: "/tmp",
		GroupPath:   "group",
		Tool:        "shell",
		Status:      "idle",
		CreatedAt:   time.Now(),
		ToolData:    json.RawMessage("{}"),
	}); err != nil {
		t.Fatalf("SaveInstance: %v", err)
	}
	db1.Close()

	// Reopen and verify
	db2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	defer db2.Close()
	if err := db2.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	rows, err := db2.LoadInstances()
	if err != nil {
		t.Fatalf("LoadInstances: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("Expected 1 instance, got %d", len(rows))
	}
	if rows[0].ID != "test-1" || rows[0].Title != "Test" {
		t.Errorf("Unexpected data: %+v", rows[0])
	}
}

func TestSaveLoadInstances(t *testing.T) {
	db := newTestDB(t)

	now := time.Now()
	instances := []*InstanceRow{
		{ID: "a", Title: "Alpha", ProjectPath: "/a", GroupPath: "grp", Order: 0, Tool: "claude", Status: "idle", CreatedAt: now, ToolData: json.RawMessage(`{"claude_session_id":"abc"}`)},
		{ID: "b", Title: "Beta", ProjectPath: "/b", GroupPath: "grp", Order: 1, Tool: "gemini", Status: "running", CreatedAt: now, ToolData: json.RawMessage("{}")},
	}

	if err := db.SaveInstances(instances); err != nil {
		t.Fatalf("SaveInstances: %v", err)
	}

	loaded, err := db.LoadInstances()
	if err != nil {
		t.Fatalf("LoadInstances: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("Expected 2 instances, got %d", len(loaded))
	}
	if loaded[0].ID != "a" || loaded[1].ID != "b" {
		t.Errorf("Wrong order: %s, %s", loaded[0].ID, loaded[1].ID)
	}
	if loaded[0].Tool != "claude" {
		t.Errorf("Expected tool 'claude', got %q", loaded[0].Tool)
	}

	// Verify tool_data round-trip
	if string(loaded[0].ToolData) != `{"claude_session_id":"abc"}` {
		t.Errorf("ToolData mismatch: %s", loaded[0].ToolData)
	}
}

func TestSaveLoadGroups(t *testing.T) {
	db := newTestDB(t)

	groups := []*GroupRow{
		{Path: "projects", Name: "Projects", Expanded: true, Order: 0},
		{Path: "personal", Name: "Personal", Expanded: false, Order: 1, DefaultPath: "/home"},
	}

	if err := db.SaveGroups(groups); err != nil {
		t.Fatalf("SaveGroups: %v", err)
	}

	loaded, err := db.LoadGroups()
	if err != nil {
		t.Fatalf("LoadGroups: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("Expected 2 groups, got %d", len(loaded))
	}
	if !loaded[0].Expanded || loaded[1].Expanded {
		t.Errorf("Expanded mismatch: %v, %v", loaded[0].Expanded, loaded[1].Expanded)
	}
	if loaded[1].DefaultPath != "/home" {
		t.Errorf("DefaultPath: %q", loaded[1].DefaultPath)
	}
}

func TestDeleteInstance(t *testing.T) {
	db := newTestDB(t)

	if err := db.SaveInstance(&InstanceRow{
		ID: "del-me", Title: "Delete Me", ProjectPath: "/tmp", GroupPath: "grp",
		Tool: "shell", Status: "idle", CreatedAt: time.Now(), ToolData: json.RawMessage("{}"),
	}); err != nil {
		t.Fatalf("SaveInstance: %v", err)
	}

	if err := db.DeleteInstance("del-me"); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}

	rows, _ := db.LoadInstances()
	if len(rows) != 0 {
		t.Errorf("Expected 0 instances after delete, got %d", len(rows))
	}
}

func TestStatusReadWrite(t *testing.T) {
	db := newTestDB(t)

	// Insert instance first
	if err := db.SaveInstance(&InstanceRow{
		ID: "s1", Title: "S1", ProjectPath: "/tmp", GroupPath: "grp",
		Tool: "claude", Status: "idle", CreatedAt: time.Now(), ToolData: json.RawMessage("{}"),
	}); err != nil {
		t.Fatalf("SaveInstance: %v", err)
	}

	// Simulate previously acknowledged waiting/idle state.
	if err := db.SetAcknowledged("s1", true); err != nil {
		t.Fatalf("SetAcknowledged: %v", err)
	}

	// Write status
	if err := db.WriteStatus("s1", "running", "claude"); err != nil {
		t.Fatalf("WriteStatus: %v", err)
	}

	// Read back
	statuses, err := db.ReadAllStatuses()
	if err != nil {
		t.Fatalf("ReadAllStatuses: %v", err)
	}
	if s, ok := statuses["s1"]; !ok || s.Status != "running" || s.Tool != "claude" {
		t.Errorf("Unexpected status: %+v", statuses["s1"])
	}
	if statuses["s1"].Acknowledged {
		t.Error("running status should clear acknowledged flag")
	}
}

func TestAcknowledgedSync(t *testing.T) {
	db := newTestDB(t)

	if err := db.SaveInstance(&InstanceRow{
		ID: "ack1", Title: "Ack Test", ProjectPath: "/tmp", GroupPath: "grp",
		Tool: "shell", Status: "waiting", CreatedAt: time.Now(), ToolData: json.RawMessage("{}"),
	}); err != nil {
		t.Fatalf("SaveInstance: %v", err)
	}

	// Set acknowledged from "instance A"
	if err := db.SetAcknowledged("ack1", true); err != nil {
		t.Fatalf("SetAcknowledged: %v", err)
	}

	// Read from "instance B" - should see the ack
	statuses, err := db.ReadAllStatuses()
	if err != nil {
		t.Fatalf("ReadAllStatuses: %v", err)
	}
	if !statuses["ack1"].Acknowledged {
		t.Error("Expected acknowledged=true after SetAcknowledged")
	}

	// Clear ack
	if err := db.SetAcknowledged("ack1", false); err != nil {
		t.Fatalf("SetAcknowledged(false): %v", err)
	}
	statuses, _ = db.ReadAllStatuses()
	if statuses["ack1"].Acknowledged {
		t.Error("Expected acknowledged=false after clearing")
	}
}

func TestHeartbeat(t *testing.T) {
	db := newTestDB(t)

	// Register
	if err := db.RegisterInstance(true); err != nil {
		t.Fatalf("RegisterInstance: %v", err)
	}

	// Heartbeat
	if err := db.Heartbeat(); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	// Check alive count
	count, err := db.AliveInstanceCount()
	if err != nil {
		t.Fatalf("AliveInstanceCount: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 alive, got %d", count)
	}

	// Unregister
	if err := db.UnregisterInstance(); err != nil {
		t.Fatalf("UnregisterInstance: %v", err)
	}

	count, _ = db.AliveInstanceCount()
	if count != 0 {
		t.Errorf("Expected 0 alive after unregister, got %d", count)
	}
}

func TestHeartbeatCleanup(t *testing.T) {
	db := newTestDB(t)

	// Insert a fake stale heartbeat (pid=99999, heartbeat 2 minutes ago)
	stale := time.Now().Add(-2 * time.Minute).Unix()
	_, err := db.DB().Exec(
		"INSERT INTO instance_heartbeats (pid, started, heartbeat, is_primary) VALUES (?, ?, ?, ?)",
		99999, stale, stale, 0,
	)
	if err != nil {
		t.Fatalf("Insert stale: %v", err)
	}

	// Register our own (fresh)
	if err := db.RegisterInstance(false); err != nil {
		t.Fatalf("RegisterInstance: %v", err)
	}

	// Clean dead (30s timeout should remove the stale one)
	if err := db.CleanDeadInstances(30 * time.Second); err != nil {
		t.Fatalf("CleanDeadInstances: %v", err)
	}

	// Only our instance should remain
	count, _ := db.AliveInstanceCount()
	if count != 1 {
		t.Errorf("Expected 1 alive after cleanup, got %d", count)
	}
}

func TestTouchAndLastModified(t *testing.T) {
	db := newTestDB(t)

	// Initially no timestamp
	ts0, err := db.LastModified()
	if err != nil {
		t.Fatalf("LastModified: %v", err)
	}
	if ts0 != 0 {
		t.Errorf("Expected 0 before any touch, got %d", ts0)
	}

	// Touch
	if err := db.Touch(); err != nil {
		t.Fatalf("Touch: %v", err)
	}

	ts1, err := db.LastModified()
	if err != nil {
		t.Fatalf("LastModified: %v", err)
	}
	if ts1 == 0 {
		t.Error("Expected non-zero after touch")
	}

	// Touch again (should advance)
	time.Sleep(2 * time.Millisecond) // ensure different nanosecond
	if err := db.Touch(); err != nil {
		t.Fatalf("Touch: %v", err)
	}

	ts2, _ := db.LastModified()
	if ts2 <= ts1 {
		t.Errorf("Expected ts2 > ts1: %d <= %d", ts2, ts1)
	}
}

func TestToolDataJSON(t *testing.T) {
	db := newTestDB(t)

	toolData := json.RawMessage(`{
		"claude_session_id": "cls-abc123",
		"gemini_session_id": "gem-xyz789",
		"gemini_yolo_mode": true,
		"latest_prompt": "fix the auth bug",
		"loaded_mcp_names": ["github", "exa"]
	}`)

	if err := db.SaveInstance(&InstanceRow{
		ID: "json1", Title: "JSON Test", ProjectPath: "/tmp", GroupPath: "grp",
		Tool: "claude", Status: "idle", CreatedAt: time.Now(), ToolData: toolData,
	}); err != nil {
		t.Fatalf("SaveInstance: %v", err)
	}

	loaded, err := db.LoadInstances()
	if err != nil {
		t.Fatalf("LoadInstances: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("Expected 1, got %d", len(loaded))
	}

	// Parse the JSON to verify structure
	var parsed map[string]any
	if err := json.Unmarshal(loaded[0].ToolData, &parsed); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if parsed["claude_session_id"] != "cls-abc123" {
		t.Errorf("claude_session_id: %v", parsed["claude_session_id"])
	}
	if parsed["gemini_yolo_mode"] != true {
		t.Errorf("gemini_yolo_mode: %v", parsed["gemini_yolo_mode"])
	}
}

func TestConcurrentAccess(t *testing.T) {
	db := newTestDB(t)

	// Pre-insert instances
	for i := 0; i < 10; i++ {
		id := "concurrent-" + string(rune('a'+i))
		if err := db.SaveInstance(&InstanceRow{
			ID: id, Title: id, ProjectPath: "/tmp", GroupPath: "grp",
			Tool: "shell", Status: "idle", CreatedAt: time.Now(), ToolData: json.RawMessage("{}"),
		}); err != nil {
			t.Fatalf("SaveInstance: %v", err)
		}
	}

	// Concurrent readers and writers
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_, _ = db.LoadInstances()
				_, _ = db.ReadAllStatuses()
			}
		}()
	}

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				id := "concurrent-" + string(rune('a'+idx))
				_ = db.WriteStatus(id, "running", "shell")
				_ = db.Heartbeat()
				_ = db.Touch()
			}
		}(i)
	}

	wg.Wait()
}

func TestIsEmpty(t *testing.T) {
	db := newTestDB(t)

	empty, err := db.IsEmpty()
	if err != nil {
		t.Fatalf("IsEmpty: %v", err)
	}
	if !empty {
		t.Error("Expected empty db")
	}

	if err := db.SaveInstance(&InstanceRow{
		ID: "not-empty", Title: "X", ProjectPath: "/tmp", GroupPath: "grp",
		Tool: "shell", Status: "idle", CreatedAt: time.Now(), ToolData: json.RawMessage("{}"),
	}); err != nil {
		t.Fatalf("SaveInstance: %v", err)
	}

	empty, _ = db.IsEmpty()
	if empty {
		t.Error("Expected non-empty after insert")
	}
}

func TestMetadata(t *testing.T) {
	db := newTestDB(t)

	// Missing key returns empty
	val, err := db.GetMeta("nonexistent")
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if val != "" {
		t.Errorf("Expected empty, got %q", val)
	}

	// Set and get
	if err := db.SetMeta("test_key", "test_value"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	val, _ = db.GetMeta("test_key")
	if val != "test_value" {
		t.Errorf("Expected 'test_value', got %q", val)
	}

	// Overwrite
	if err := db.SetMeta("test_key", "new_value"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	val, _ = db.GetMeta("test_key")
	if val != "new_value" {
		t.Errorf("Expected 'new_value', got %q", val)
	}
}

func TestElectPrimary_FirstInstance(t *testing.T) {
	db := newTestDB(t)

	// Register and elect
	if err := db.RegisterInstance(false); err != nil {
		t.Fatalf("RegisterInstance: %v", err)
	}
	isPrimary, err := db.ElectPrimary(30 * time.Second)
	if err != nil {
		t.Fatalf("ElectPrimary: %v", err)
	}
	if !isPrimary {
		t.Error("First instance should become primary")
	}

	// Calling again should still return true (already primary)
	isPrimary, err = db.ElectPrimary(30 * time.Second)
	if err != nil {
		t.Fatalf("ElectPrimary (repeat): %v", err)
	}
	if !isPrimary {
		t.Error("Should still be primary on repeat call")
	}
}

func TestElectPrimary_SecondInstance(t *testing.T) {
	db := newTestDB(t)

	// Simulate first instance (PID 10001) as primary with fresh heartbeat
	now := time.Now().Unix()
	_, err := db.DB().Exec(
		"INSERT INTO instance_heartbeats (pid, started, heartbeat, is_primary) VALUES (?, ?, ?, ?)",
		10001, now, now, 1,
	)
	if err != nil {
		t.Fatalf("Insert primary: %v", err)
	}

	// Register our process (not primary yet)
	if err := db.RegisterInstance(false); err != nil {
		t.Fatalf("RegisterInstance: %v", err)
	}

	// Try to elect: should fail because PID 10001 is alive and primary
	isPrimary, err := db.ElectPrimary(30 * time.Second)
	if err != nil {
		t.Fatalf("ElectPrimary: %v", err)
	}
	if isPrimary {
		t.Error("Second instance should NOT become primary while first is alive")
	}
}

func TestElectPrimary_Failover(t *testing.T) {
	db := newTestDB(t)

	// Simulate a stale primary (heartbeat 2 minutes ago)
	stale := time.Now().Add(-2 * time.Minute).Unix()
	_, err := db.DB().Exec(
		"INSERT INTO instance_heartbeats (pid, started, heartbeat, is_primary) VALUES (?, ?, ?, ?)",
		10001, stale, stale, 1,
	)
	if err != nil {
		t.Fatalf("Insert stale primary: %v", err)
	}

	// Register our process
	if err := db.RegisterInstance(false); err != nil {
		t.Fatalf("RegisterInstance: %v", err)
	}

	// Elect: stale primary should be cleared, we should become primary
	isPrimary, err := db.ElectPrimary(30 * time.Second)
	if err != nil {
		t.Fatalf("ElectPrimary: %v", err)
	}
	if !isPrimary {
		t.Error("Should become primary after stale primary is cleared")
	}

	// Verify the stale PID is no longer primary
	var stalePrimary int
	err = db.DB().QueryRow(
		"SELECT is_primary FROM instance_heartbeats WHERE pid = 10001",
	).Scan(&stalePrimary)
	if err != nil {
		t.Fatalf("Query stale PID: %v", err)
	}
	if stalePrimary != 0 {
		t.Error("Stale PID should have is_primary=0")
	}
}

func TestResignPrimary(t *testing.T) {
	db := newTestDB(t)

	// Register and elect
	if err := db.RegisterInstance(false); err != nil {
		t.Fatalf("RegisterInstance: %v", err)
	}
	isPrimary, err := db.ElectPrimary(30 * time.Second)
	if err != nil {
		t.Fatalf("ElectPrimary: %v", err)
	}
	if !isPrimary {
		t.Fatal("Should be primary")
	}

	// Resign
	if err := db.ResignPrimary(); err != nil {
		t.Fatalf("ResignPrimary: %v", err)
	}

	// Verify we're no longer primary
	var isPrim int
	err = db.DB().QueryRow(
		"SELECT is_primary FROM instance_heartbeats WHERE pid = ?",
		db.pid,
	).Scan(&isPrim)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if isPrim != 0 {
		t.Error("Should not be primary after resign")
	}

	// Re-elect should work since no primary exists
	isPrimary, err = db.ElectPrimary(30 * time.Second)
	if err != nil {
		t.Fatalf("ElectPrimary after resign: %v", err)
	}
	if !isPrimary {
		t.Error("Should become primary again after resign")
	}
}

func TestGlobalSingleton(t *testing.T) {
	// Initially nil
	if GetGlobal() != nil {
		t.Error("Expected nil global initially")
	}

	db := newTestDB(t)
	SetGlobal(db)
	defer SetGlobal(nil) // cleanup

	if GetGlobal() != db {
		t.Error("Expected global to return the set db")
	}

	SetGlobal(nil)
	if GetGlobal() != nil {
		t.Error("Expected nil after clearing")
	}
}

func TestRecentSessions_DedupUsesFullConfig(t *testing.T) {
	db := newTestDB(t)

	common := RecentSessionRow{
		Title:       "same-title",
		ProjectPath: "/tmp/project",
		GroupPath:   "default",
		Tool:        "claude",
	}
	rowA := common
	rowA.Command = "claude --one"
	rowA.ToolOptions = json.RawMessage(`{"tool":"claude","options":{"skip_permissions":true}}`)

	rowB := common
	rowB.Command = "claude --two" // differs from rowA
	rowB.ToolOptions = json.RawMessage(`{"tool":"claude","options":{"skip_permissions":true}}`)

	rowC := common
	rowC.Command = "claude --one"
	rowC.ToolOptions = json.RawMessage(`{"tool":"claude","options":{"skip_permissions":false}}`) // differs from rowA

	if err := db.SaveRecentSession(&rowA); err != nil {
		t.Fatalf("SaveRecentSession(rowA): %v", err)
	}
	if err := db.SaveRecentSession(&rowB); err != nil {
		t.Fatalf("SaveRecentSession(rowB): %v", err)
	}
	if err := db.SaveRecentSession(&rowC); err != nil {
		t.Fatalf("SaveRecentSession(rowC): %v", err)
	}

	rows, err := db.LoadRecentSessions()
	if err != nil {
		t.Fatalf("LoadRecentSessions: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 distinct rows, got %d", len(rows))
	}
}

func TestRecentSessions_DedupIdenticalConfig(t *testing.T) {
	db := newTestDB(t)

	yolo := true
	row := &RecentSessionRow{
		Title:          "same-title",
		ProjectPath:    "/tmp/project",
		GroupPath:      "default",
		Command:        "claude --resume abc",
		Wrapper:        "wrapper.sh",
		Tool:           "claude",
		ToolOptions:    json.RawMessage(`{"tool":"claude","options":{"session_mode":"resume","resume_session_id":"abc"}}`),
		SandboxEnabled: true,
		GeminiYoloMode: &yolo,
	}

	if err := db.SaveRecentSession(row); err != nil {
		t.Fatalf("SaveRecentSession(first): %v", err)
	}
	if err := db.SaveRecentSession(row); err != nil {
		t.Fatalf("SaveRecentSession(second): %v", err)
	}

	rows, err := db.LoadRecentSessions()
	if err != nil {
		t.Fatalf("LoadRecentSessions: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected deduped row count 1, got %d", len(rows))
	}
}
