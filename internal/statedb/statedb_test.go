package statedb

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
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

// --- Schema Migration Tests ---
// These tests verify that Migrate() correctly upgrades databases created with older schemas.
// Incident (2026-03-26): PR #385 added the "acknowledged" column without an ALTER TABLE
// migration, breaking all existing users upgrading from v0.26.x.

// createV1SchemaDB creates a database with the v0.26.x schema (before "acknowledged" column,
// before recent_sessions, before cost_events). Returns an open *StateDB.
func createV1SchemaDB(t *testing.T) *StateDB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")

	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := rawDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatalf("WAL: %v", err)
	}

	// v1 schema: instances WITHOUT acknowledged column, no recent_sessions, no cost_events
	for _, stmt := range []string{
		`CREATE TABLE metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`INSERT INTO metadata (key, value) VALUES ('schema_version', '1')`,
		`CREATE TABLE instances (
			id              TEXT PRIMARY KEY,
			title           TEXT NOT NULL,
			project_path    TEXT NOT NULL,
			group_path      TEXT NOT NULL DEFAULT 'my-sessions',
			sort_order      INTEGER NOT NULL DEFAULT 0,
			command         TEXT NOT NULL DEFAULT '',
			wrapper         TEXT NOT NULL DEFAULT '',
			tool            TEXT NOT NULL DEFAULT 'shell',
			status          TEXT NOT NULL DEFAULT 'error',
			tmux_session    TEXT NOT NULL DEFAULT '',
			created_at      INTEGER NOT NULL,
			last_accessed   INTEGER NOT NULL DEFAULT 0,
			parent_session_id TEXT NOT NULL DEFAULT '',
			worktree_path     TEXT NOT NULL DEFAULT '',
			worktree_repo     TEXT NOT NULL DEFAULT '',
			worktree_branch   TEXT NOT NULL DEFAULT '',
			tool_data       TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE TABLE groups (
			path         TEXT PRIMARY KEY,
			name         TEXT NOT NULL,
			expanded     INTEGER NOT NULL DEFAULT 1,
			sort_order   INTEGER NOT NULL DEFAULT 0,
			default_path TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE instance_heartbeats (
			pid        INTEGER PRIMARY KEY,
			started    INTEGER NOT NULL,
			heartbeat  INTEGER NOT NULL,
			is_primary INTEGER NOT NULL DEFAULT 0
		)`,
	} {
		if _, err := rawDB.Exec(stmt); err != nil {
			t.Fatalf("create v1 schema: %v\nSQL: %s", err, stmt)
		}
	}

	// Insert a session to simulate existing user data
	now := time.Now().Unix()
	if _, err := rawDB.Exec(`
		INSERT INTO instances (id, title, project_path, group_path, sort_order, tool, status, created_at, tool_data)
		VALUES ('existing-1', 'My Session', '/home/user/project', 'conductor', 0, 'claude', 'idle', ?, '{}')
	`, now); err != nil {
		t.Fatalf("insert v1 instance: %v", err)
	}

	rawDB.Close()

	// Reopen through StateDB
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open after v1 creation: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestMigrate_OldSchema_AcknowledgedColumn verifies that upgrading from v1 schema
// (without "acknowledged" column) to current schema works. This is the exact scenario
// that broke all v0.26.x users in the v0.27.0 release.
func TestMigrate_OldSchema_AcknowledgedColumn(t *testing.T) {
	db := createV1SchemaDB(t)

	// Run Migrate() on the old schema: should add the acknowledged column
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate() on v1 schema failed: %v", err)
	}

	// Verify existing data survived the migration
	instances, err := db.LoadInstances()
	if err != nil {
		t.Fatalf("LoadInstances after migrate: %v", err)
	}
	if len(instances) != 1 {
		t.Fatalf("expected 1 instance after migrate, got %d", len(instances))
	}
	if instances[0].ID != "existing-1" || instances[0].Title != "My Session" {
		t.Errorf("instance data corrupted: %+v", instances[0])
	}

	// Verify acknowledged column works (the exact operation that broke in v0.27.0)
	if err := db.SetAcknowledged("existing-1", true); err != nil {
		t.Fatalf("SetAcknowledged after migrate: %v", err)
	}

	statuses, err := db.ReadAllStatuses()
	if err != nil {
		t.Fatalf("ReadAllStatuses after migrate: %v", err)
	}
	if !statuses["existing-1"].Acknowledged {
		t.Error("expected acknowledged=true after SetAcknowledged on migrated DB")
	}

	// Verify WriteStatus also works (clears acknowledged when running)
	if err := db.WriteStatus("existing-1", "running", "claude"); err != nil {
		t.Fatalf("WriteStatus after migrate: %v", err)
	}
	statuses, _ = db.ReadAllStatuses()
	if statuses["existing-1"].Acknowledged {
		t.Error("running status should clear acknowledged flag on migrated DB")
	}
}

// TestMigrate_OldSchema_NewTablesCreated verifies that new tables (recent_sessions,
// cost_events) are created when migrating from v1 schema.
func TestMigrate_OldSchema_NewTablesCreated(t *testing.T) {
	db := createV1SchemaDB(t)

	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate() on v1 schema failed: %v", err)
	}

	// Verify recent_sessions table was created and works
	if err := db.SaveRecentSession(&RecentSessionRow{
		Title:       "test-recent",
		ProjectPath: "/tmp",
		GroupPath:   "default",
		Tool:        "claude",
		ToolOptions: json.RawMessage("{}"),
	}); err != nil {
		t.Fatalf("SaveRecentSession on migrated DB: %v", err)
	}

	recent, err := db.LoadRecentSessions()
	if err != nil {
		t.Fatalf("LoadRecentSessions on migrated DB: %v", err)
	}
	if len(recent) != 1 {
		t.Errorf("expected 1 recent session, got %d", len(recent))
	}

	// Verify cost_events table was created (just check it's queryable)
	var count int
	if err := db.DB().QueryRow("SELECT COUNT(*) FROM cost_events").Scan(&count); err != nil {
		t.Fatalf("cost_events table not created by Migrate(): %v", err)
	}
}

// TestMigrate_OldSchema_NewInstanceCreation verifies that creating a NEW instance works
// on a migrated v1 database. This catches issues where INSERT statements reference
// columns that don't exist in the upgraded schema.
func TestMigrate_OldSchema_NewInstanceCreation(t *testing.T) {
	db := createV1SchemaDB(t)

	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate() on v1 schema failed: %v", err)
	}

	// Create a new instance (simulates what the TUI does when user creates a session)
	if err := db.SaveInstance(&InstanceRow{
		ID:          "new-after-migrate",
		Title:       "New Session",
		ProjectPath: "/tmp/new",
		GroupPath:   "conductor",
		Tool:        "claude",
		Status:      "starting",
		CreatedAt:   time.Now(),
		ToolData:    json.RawMessage(`{"claude_session_id":"test"}`),
	}); err != nil {
		t.Fatalf("SaveInstance on migrated DB: %v", err)
	}

	// Load and verify both old and new instances exist
	instances, err := db.LoadInstances()
	if err != nil {
		t.Fatalf("LoadInstances: %v", err)
	}
	if len(instances) != 2 {
		t.Fatalf("expected 2 instances (1 old + 1 new), got %d", len(instances))
	}
}

// TestMigrate_OldSchema_SchemaVersionUpdated verifies the schema version is bumped after migration.
func TestMigrate_OldSchema_SchemaVersionUpdated(t *testing.T) {
	db := createV1SchemaDB(t)

	// Verify pre-migration version
	preVersion, err := db.GetMeta("schema_version")
	if err != nil {
		t.Fatalf("GetMeta before migrate: %v", err)
	}
	if preVersion != "1" {
		t.Fatalf("expected schema_version=1 before migrate, got %q", preVersion)
	}

	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate(): %v", err)
	}

	// Verify post-migration version matches current SchemaVersion
	postVersion, err := db.GetMeta("schema_version")
	if err != nil {
		t.Fatalf("GetMeta after migrate: %v", err)
	}
	expected := "4" // current SchemaVersion
	if postVersion != expected {
		t.Errorf("expected schema_version=%s after migrate, got %q", expected, postVersion)
	}
}

// TestMigrate_Idempotent verifies that running Migrate() twice on the same DB is safe.
func TestMigrate_Idempotent(t *testing.T) {
	db := createV1SchemaDB(t)

	// First migration
	if err := db.Migrate(); err != nil {
		t.Fatalf("first Migrate(): %v", err)
	}

	// Second migration (should be a no-op, not error)
	if err := db.Migrate(); err != nil {
		t.Fatalf("second Migrate() failed (not idempotent): %v", err)
	}

	// Verify data is intact
	instances, err := db.LoadInstances()
	if err != nil {
		t.Fatalf("LoadInstances after double migrate: %v", err)
	}
	if len(instances) != 1 {
		t.Errorf("expected 1 instance after double migrate, got %d", len(instances))
	}
}
