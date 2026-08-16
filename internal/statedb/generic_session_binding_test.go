package statedb

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openTestDB(t *testing.T) *StateDB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func seedInstance(t *testing.T, db *StateDB, id string, toolData json.RawMessage) {
	t.Helper()
	now := time.Now()
	row := &InstanceRow{
		ID: id, Title: id, Status: "idle", Tool: "shell",
		CreatedAt: now, LastAccessed: now, GroupPath: "default",
		ToolData: toolData,
	}
	if err := db.SaveInstance(row); err != nil {
		t.Fatalf("SaveInstance: %v", err)
	}
}

func readToolData(t *testing.T, db *StateDB, id string) map[string]json.RawMessage {
	t.Helper()
	var raw sql.NullString
	if err := db.DB().QueryRow(`SELECT tool_data FROM instances WHERE id = ?`, id).Scan(&raw); err != nil {
		t.Fatalf("SELECT tool_data: %v", err)
	}
	m := map[string]json.RawMessage{}
	if raw.Valid && raw.String != "" {
		if err := json.Unmarshal([]byte(raw.String), &m); err != nil {
			t.Fatalf("parse tool_data: %v", err)
		}
	}
	return m
}

func TestWriteGenericSessionBinding_NoClobberSiblings(t *testing.T) {
	db := openTestDB(t)
	seedInstance(t, db, "i1", json.RawMessage(`{
		"color":"#abc",
		"claude_session_id":"c-keep",
		"last_started_at":123
	}`))

	if err := db.WriteGenericSessionBinding("i1", "g-1", "mytool", "mytool --flag", "local:/tmp/proj", time.Unix(50, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	m := readToolData(t, db, "i1")
	if string(m["generic_session_id"]) != `"g-1"` {
		t.Fatalf("generic=%s", m["generic_session_id"])
	}
	if string(m["claude_session_id"]) != `"c-keep"` {
		t.Fatalf("claude clobbered: %s", m["claude_session_id"])
	}
	if string(m["color"]) != `"#abc"` {
		t.Fatalf("color clobbered: %s", m["color"])
	}
	if string(m["last_started_at"]) != `123` {
		t.Fatalf("last_started_at clobbered: %s", m["last_started_at"])
	}

	if err := db.WriteGenericSessionBinding("i1", "", "mytool", "mytool --flag", "local:/tmp/proj", time.Time{}); err != nil {
		t.Fatal(err)
	}
	m = readToolData(t, db, "i1")
	if _, ok := m["generic_session_id"]; ok {
		t.Fatalf("generic still present: %v", m)
	}
	if _, ok := m["generic_detected_at"]; ok {
		t.Fatalf("detected_at still present: %v", m)
	}
	if string(m["claude_session_id"]) != `"c-keep"` {
		t.Fatalf("clear clobbered claude: %s", m["claude_session_id"])
	}
}

func TestWriteGenericSessionBinding_ZeroDetectedAtStampsNow(t *testing.T) {
	db := openTestDB(t)
	seedInstance(t, db, "i1", json.RawMessage(`{}`))
	before := time.Now().Add(-2 * time.Second).Unix()
	if err := db.WriteGenericSessionBinding("i1", "sid", "mytool", "mytool --flag", "local:/tmp/proj", time.Time{}); err != nil {
		t.Fatal(err)
	}
	after := time.Now().Add(2 * time.Second).Unix()
	m := readToolData(t, db, "i1")
	var at int64
	if err := json.Unmarshal(m["generic_detected_at"], &at); err != nil {
		t.Fatal(err)
	}
	if at < before || at > after {
		t.Fatalf("detected_at=%d outside [%d,%d]", at, before, after)
	}
}

func TestWriteGenericSessionBinding_ConcurrentLastWins(t *testing.T) {
	db := openTestDB(t)
	seedInstance(t, db, "i1", json.RawMessage(`{}`))

	const n = 50
	var wg sync.WaitGroup
	errCh := make(chan error, 2*n)
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := db.WriteGenericSessionBinding("i1", "A", "mytool", "mytool --flag", "local:/tmp/proj", time.Now()); err != nil {
				errCh <- err
			}
		}()
		go func() {
			defer wg.Done()
			if err := db.WriteGenericSessionBinding("i1", "B", "mytool", "mytool --flag", "local:/tmp/proj", time.Now()); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	m := readToolData(t, db, "i1")
	got := string(m["generic_session_id"])
	if got != `"A"` && got != `"B"` {
		t.Fatalf("final id %s not A or B", got)
	}
}

func TestMergeToolDataExtras_GenericStickyAndExplicitClear(t *testing.T) {
	old := json.RawMessage(`{"generic_session_id":"keep","generic_detected_at":9,"color":"#x"}`)

	// Omission preserves (stale full save).
	merged := MergeToolDataExtras(old, json.RawMessage(`{"color":"#y"}`))
	var m map[string]json.RawMessage
	_ = json.Unmarshal(merged, &m)
	if string(m["generic_session_id"]) != `"keep"` {
		t.Fatalf("sticky omit: %s", merged)
	}
	if string(m["color"]) != `"#y"` {
		t.Fatalf("color: %s", merged)
	}

	// Explicit empty clears.
	merged = MergeToolDataExtras(old, json.RawMessage(`{"generic_session_id":"","generic_detected_at":0}`))
	_ = json.Unmarshal(merged, &m)
	if string(m["generic_session_id"]) != `""` {
		t.Fatalf("explicit empty: %s", merged)
	}

	// Non-empty new wins.
	merged = MergeToolDataExtras(old, json.RawMessage(`{"generic_session_id":"new"}`))
	_ = json.Unmarshal(merged, &m)
	if string(m["generic_session_id"]) != `"new"` {
		t.Fatalf("new wins: %s", merged)
	}
}

func TestSaveInstances_StickyGenericAcrossBatch(t *testing.T) {
	db := openTestDB(t)
	now := time.Now()
	a := &InstanceRow{
		ID: "a", Title: "a", Status: "idle", Tool: "shell",
		CreatedAt: now, LastAccessed: now, GroupPath: "default",
		ToolData: json.RawMessage(`{"generic_session_id":"sid-a","color":"#1"}`),
	}
	if err := db.SaveInstances([]*InstanceRow{a}); err != nil {
		t.Fatal(err)
	}

	// Batch save omits generic_session_id (stale snapshot).
	a.ToolData = json.RawMessage(`{"color":"#2"}`)
	if err := db.SaveInstances([]*InstanceRow{a}); err != nil {
		t.Fatal(err)
	}
	m := readToolData(t, db, "a")
	if string(m["generic_session_id"]) != `"sid-a"` {
		t.Fatalf("batch sticky failed: %v", m)
	}
	if string(m["color"]) != `"#2"` {
		t.Fatalf("color not updated: %v", m)
	}
}

// TestSchemaExtrasZone_NoMigrationRequired documents that generic_session_id
// lives in tool_data JSON (extras zone), not a typed SQL column — no schema
// version bump is required for this feature.
func TestSchemaExtrasZone_NoMigrationRequired(t *testing.T) {
	keys := toolDataKnownKeys()
	if keys["generic_session_id"] {
		// If this becomes a typed toolDataBlob field, stickyToolDataKeys still
		// protects it, but the design currently treats it as extras-zone only.
		t.Log("generic_session_id is now a typed toolDataBlob key — sticky list still applies")
	}
	sticky := stickyToolDataKeys()
	if !sticky["generic_session_id"] || !sticky["generic_detected_at"] {
		t.Fatal("generic_session_id must remain in stickyToolDataKeys")
	}
	// Opening + Migrate on a fresh DB succeeds without special generic migration.
	db := openTestDB(t)
	seedInstance(t, db, "x", json.RawMessage(`{}`))
	if err := db.WriteGenericSessionBinding("x", "ok", "mytool", "mytool --flag", "local:/tmp/proj", time.Now()); err != nil {
		t.Fatal(err)
	}
}

// TestWriteGenericSessionBinding_EmptyObjectAndJSONNull documents COALESCE
// behavior with modernc.org/sqlite JSON1:
//   - Schema: tool_data TEXT NOT NULL DEFAULT '{}' — SQL NULL is rejected.
//   - COALESCE(tool_data,'{}') covers hypothetical NULL (corrupt restore).
//   - An empty tool_data string is NOT replaced by COALESCE and json_set fails
//     ("malformed JSON") — same residual risk as Claude/Gemini bindings.
//   - Binding onto '{}' works (happy path for fresh rows).
func TestWriteGenericSessionBinding_EmptyObjectAndJSONNull(t *testing.T) {
	db := openTestDB(t)
	seedInstance(t, db, "empty-obj", json.RawMessage(`{}`))
	if err := db.WriteGenericSessionBinding("empty-obj", "from-empty", "mytool", "mytool --flag", "local:/tmp/proj", time.Unix(42, 0).UTC()); err != nil {
		t.Fatalf("WriteGenericSessionBinding on '{}': %v", err)
	}
	m := readToolData(t, db, "empty-obj")
	if string(m["generic_session_id"]) != `"from-empty"` {
		t.Fatalf("after '{}': %v", m)
	}

	// SQL NULL rejected by NOT NULL (documents schema; COALESCE is belt-and-suspenders).
	if _, err := db.DB().Exec(`UPDATE instances SET tool_data = NULL WHERE id = ?`, "empty-obj"); err == nil {
		t.Fatal("expected NOT NULL constraint on tool_data; COALESCE would still save bindings if NULL were allowed")
	}

	// Empty string tool_data: json_set fails (COALESCE does not rewrite '').
	if _, err := db.DB().Exec(`UPDATE instances SET tool_data = '' WHERE id = ?`, "empty-obj"); err != nil {
		t.Fatalf("set empty string tool_data: %v", err)
	}
	if err := db.WriteGenericSessionBinding("empty-obj", "nope", "mytool", "mytool --flag", "local:/tmp/proj", time.Now()); err == nil {
		t.Fatal("expected error writing binding onto empty-string tool_data")
	}
}
