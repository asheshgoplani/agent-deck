package statedb

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteAntigravitySessionBinding(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if err := db.SaveInstance(&InstanceRow{
		ID:       "inst-agy-1",
		Title:    "agy",
		Tool:     "antigravity",
		ToolData: json.RawMessage(`{"notes":"keep"}`),
	}); err != nil {
		t.Fatalf("SaveInstance: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	if err := db.WriteAntigravitySessionBinding("inst-agy-1", "conv-agy-1", now); err != nil {
		t.Fatalf("WriteAntigravitySessionBinding: %v", err)
	}

	rows, err := db.LoadInstances()
	if err != nil || len(rows) != 1 {
		t.Fatalf("LoadInstances: rows=%d err=%v", len(rows), err)
	}
	var got map[string]any
	if err := json.Unmarshal(rows[0].ToolData, &got); err != nil {
		t.Fatalf("unmarshal tool_data: %v", err)
	}
	if got["antigravity_session_id"] != "conv-agy-1" {
		t.Fatalf("antigravity_session_id = %v, want conv-agy-1", got["antigravity_session_id"])
	}
	if got["notes"] != "keep" {
		t.Fatalf("unrelated key notes = %v, want keep", got["notes"])
	}
}
