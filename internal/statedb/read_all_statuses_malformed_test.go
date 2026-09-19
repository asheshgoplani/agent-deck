package statedb

import (
	"encoding/json"
	"testing"
	"time"
)

// Review round 3 P3-5: one instance whose tool_data is not JSON (an empty
// string, a partial external write) must not abort ReadAllStatuses for every
// other instance. Both callers (TUI status sync, transition daemon) swallow
// the error, so a wholesale failure would silently blank shared statuses,
// acknowledgments and hook-lag hydration across the profile.
func TestReadAllStatuses_MalformedToolDataRowDoesNotAbortQuery(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()
	for _, id := range []string{"good", "empty", "notjson"} {
		if err := db.SaveInstance(&InstanceRow{
			ID: id, Title: id, ProjectPath: "/tmp", GroupPath: "grp",
			Tool: "claude", Status: "waiting", CreatedAt: now, ToolData: json.RawMessage("{}"),
		}); err != nil {
			t.Fatalf("SaveInstance %s: %v", id, err)
		}
	}
	if err := db.WriteToolDataExtra("good", "hook_lag", json.RawMessage(`{"hook_ts":7,"first_idle_at":9}`)); err != nil {
		t.Fatalf("WriteToolDataExtra: %v", err)
	}
	// SaveInstance and every in-tree writer guard tool_data with '{}'; only a
	// raw UPDATE produces these rows, which is exactly the external edit the
	// guard is for.
	for id, td := range map[string]string{"empty": "", "notjson": "not json"} {
		if _, err := db.db.Exec(`UPDATE instances SET tool_data = ? WHERE id = ?`, td, id); err != nil {
			t.Fatalf("raw tool_data update %s: %v", id, err)
		}
	}

	statuses, err := db.ReadAllStatuses()
	if err != nil {
		t.Fatalf("ReadAllStatuses with malformed tool_data rows: %v", err)
	}
	if len(statuses) != 3 {
		t.Fatalf("got %d rows, want 3 (malformed rows still report status)", len(statuses))
	}
	for _, id := range []string{"empty", "notjson"} {
		row, ok := statuses[id]
		if !ok || row.Status != "waiting" {
			t.Fatalf("row %s = %+v, want status waiting", id, row)
		}
		if len(row.HookLag) != 0 {
			t.Fatalf("row %s hook_lag = %s, want none for a malformed tool_data", id, row.HookLag)
		}
	}
	if got := string(statuses["good"].HookLag); got != `{"hook_ts":7,"first_idle_at":9}` {
		t.Fatalf("good row hook_lag = %q, want the extras-zone record", got)
	}
}
