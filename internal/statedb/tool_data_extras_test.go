package statedb

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestMergeToolDataExtras_PreservesUnknownKeys(t *testing.T) {
	old := json.RawMessage(`{"claude_session_id":"abc","clear_on_compact":false}`)
	new_ := json.RawMessage(`{"claude_session_id":"def"}`)

	merged := MergeToolDataExtras(old, new_)

	var got map[string]json.RawMessage
	if err := json.Unmarshal(merged, &got); err != nil {
		t.Fatalf("merged JSON does not parse: %v", err)
	}

	// Typed key: new wins.
	if string(got["claude_session_id"]) != `"def"` {
		t.Errorf("claude_session_id = %s, want \"def\"", got["claude_session_id"])
	}
	// Unknown key: preserved from old.
	if string(got["clear_on_compact"]) != `false` {
		t.Errorf("clear_on_compact = %s, want false (preserved from old)", got["clear_on_compact"])
	}
}

func TestMergeToolDataExtras_NewExplicitWinsOverOldUnknown(t *testing.T) {
	old := json.RawMessage(`{"some_unknown_key":"v1"}`)
	new_ := json.RawMessage(`{"some_unknown_key":"v2"}`)

	merged := MergeToolDataExtras(old, new_)

	var got map[string]json.RawMessage
	_ = json.Unmarshal(merged, &got)
	if string(got["some_unknown_key"]) != `"v2"` {
		t.Errorf("some_unknown_key = %s, want \"v2\" (new explicit wins)", got["some_unknown_key"])
	}
}

func TestMergeToolDataExtras_TypedKeyAbsenceRespected(t *testing.T) {
	// When the new tool_data omits a NON-STICKY typed key (e.g., omitempty
	// zero-value), the merge must NOT carry the old value forward. The typed
	// schema is authoritative for those fields. (Sticky session-id keys are the
	// deliberate exception, covered by the sticky tests below.)
	old := json.RawMessage(`{"latest_prompt":"hello"}`)
	new_ := json.RawMessage(`{}`)

	merged := MergeToolDataExtras(old, new_)

	var got map[string]json.RawMessage
	_ = json.Unmarshal(merged, &got)
	if _, present := got["latest_prompt"]; present {
		t.Errorf("latest_prompt should be absent in merged when new omits it; got %s", got["latest_prompt"])
	}
}

func TestMergeToolDataExtras_StickySessionIDPreservedWhenNewOmits(t *testing.T) {
	// t-0133: a session-id key present in the old row but OMITTED by the new
	// blob (omitempty zero-value, e.g. a concurrent full-table save whose
	// in-memory snapshot has not yet detected the id) must be carried forward,
	// not wiped. Covers all four tools + a *_detected_at companion.
	old := json.RawMessage(`{` +
		`"claude_session_id":"c-uuid","claude_detected_at":111,` +
		`"gemini_session_id":"g-uuid",` +
		`"opencode_session_id":"o-uuid",` +
		`"codex_session_id":"x-uuid"}`)
	new_ := json.RawMessage(`{"latest_prompt":"hi"}`)

	merged := MergeToolDataExtras(old, new_)

	var got map[string]json.RawMessage
	if err := json.Unmarshal(merged, &got); err != nil {
		t.Fatalf("merged JSON does not parse: %v", err)
	}
	for k, want := range map[string]string{
		"claude_session_id":   `"c-uuid"`,
		"claude_detected_at":  `111`,
		"gemini_session_id":   `"g-uuid"`,
		"opencode_session_id": `"o-uuid"`,
		"codex_session_id":    `"x-uuid"`,
	} {
		if string(got[k]) != want {
			t.Errorf("%s = %s, want %s (sticky: preserved from old when new omits)", k, got[k], want)
		}
	}
	// The new blob's own key still lands.
	if string(got["latest_prompt"]) != `"hi"` {
		t.Errorf("latest_prompt = %s, want \"hi\"", got["latest_prompt"])
	}
}

func TestMergeToolDataExtras_StickyNonEmptyNewWins(t *testing.T) {
	// A real resume/fork writes a NEW non-empty session id; that must win over
	// the old value (sticky only protects the omission case, never overrides an
	// explicit update).
	old := json.RawMessage(`{"claude_session_id":"old-uuid"}`)
	new_ := json.RawMessage(`{"claude_session_id":"new-uuid"}`)

	merged := MergeToolDataExtras(old, new_)

	var got map[string]json.RawMessage
	_ = json.Unmarshal(merged, &got)
	if string(got["claude_session_id"]) != `"new-uuid"` {
		t.Errorf("claude_session_id = %s, want \"new-uuid\" (non-empty new wins)", got["claude_session_id"])
	}
}

func TestMergeToolDataExtras_StickyExplicitEmptyIsIntentionalClear(t *testing.T) {
	// An EXPLICIT empty value present in the new blob (not omitted) is honored
	// as an intentional clear — the escape hatch for a deliberate reset/dedup
	// that wants the id gone. Only outright omission preserves.
	old := json.RawMessage(`{"claude_session_id":"old-uuid"}`)
	new_ := json.RawMessage(`{"claude_session_id":""}`)

	merged := MergeToolDataExtras(old, new_)

	var got map[string]json.RawMessage
	_ = json.Unmarshal(merged, &got)
	if string(got["claude_session_id"]) != `""` {
		t.Errorf("claude_session_id = %s, want \"\" (explicit empty = intentional clear)", got["claude_session_id"])
	}
}

func TestMergeToolDataExtras_EmptyOld(t *testing.T) {
	new_ := json.RawMessage(`{"claude_session_id":"abc"}`)
	merged := MergeToolDataExtras(nil, new_)
	if string(merged) != string(new_) {
		t.Errorf("empty old should pass through new; got %s, want %s", merged, new_)
	}
}

func TestMergeToolDataExtras_CorruptOldFallsThrough(t *testing.T) {
	old := json.RawMessage(`not json`)
	new_ := json.RawMessage(`{"claude_session_id":"abc"}`)
	merged := MergeToolDataExtras(old, new_)
	if string(merged) != string(new_) {
		t.Errorf("corrupt old should fall through to new; got %s", merged)
	}
}

func TestToolDataKnownKeys_IncludesCoreFields(t *testing.T) {
	keys := toolDataKnownKeys()
	for _, expected := range []string{
		"claude_session_id",
		"codex_session_id",
		"latest_prompt",
		"color",
	} {
		if !keys[expected] {
			t.Errorf("toolDataKnownKeys missing expected typed key %q", expected)
		}
	}
}

// TestSaveInstance_PreservesClearOnCompactExtra is the regression test for
// the bug surfaced 2026-04-29: a manually-written tool_data extra
// (clear_on_compact) was being silently dropped on every SaveInstance
// because INSERT OR REPLACE wholesale-replaced the row's tool_data with
// the typed-only blob.
func TestSaveInstance_PreservesClearOnCompactExtra(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	id := "test-instance-1"
	now := time.Now()
	row := &InstanceRow{
		ID:           id,
		Title:        "test",
		Status:       "running",
		Tool:         "claude",
		CreatedAt:    now,
		LastAccessed: now,
		GroupPath:    "default",
		ToolData:     json.RawMessage(`{"claude_session_id":"abc"}`),
	}
	if err := db.SaveInstance(row); err != nil {
		t.Fatalf("first SaveInstance failed: %v", err)
	}

	// Simulate a manual SQLite write adding clear_on_compact (the user's
	// canonical method per standing convention).
	if _, err := db.DB().Exec(
		`UPDATE instances SET tool_data = json_set(tool_data, '$.clear_on_compact', json('false')) WHERE id = ?`,
		id,
	); err != nil {
		t.Fatalf("manual update failed: %v", err)
	}

	// Verify the manual write landed.
	var afterManual sql.NullString
	if err := db.DB().QueryRow("SELECT tool_data FROM instances WHERE id = ?", id).Scan(&afterManual); err != nil {
		t.Fatalf("read after manual update: %v", err)
	}
	if !afterManual.Valid {
		t.Fatal("tool_data is null after manual update")
	}
	var afterManualMap map[string]json.RawMessage
	_ = json.Unmarshal([]byte(afterManual.String), &afterManualMap)
	if string(afterManualMap["clear_on_compact"]) != "false" {
		t.Fatalf("manual write did not land: tool_data=%s", afterManual.String)
	}

	// Now agent-deck saves the row again with a typed-only blob (e.g., a
	// new claude_session_id from a fresh detection). Pre-fix this would
	// wipe clear_on_compact; post-fix it must be preserved.
	row.ToolData = json.RawMessage(`{"claude_session_id":"def"}`)
	if err := db.SaveInstance(row); err != nil {
		t.Fatalf("second SaveInstance failed: %v", err)
	}

	var afterReSave sql.NullString
	if err := db.DB().QueryRow("SELECT tool_data FROM instances WHERE id = ?", id).Scan(&afterReSave); err != nil {
		t.Fatalf("read after re-save: %v", err)
	}
	var afterReSaveMap map[string]json.RawMessage
	if err := json.Unmarshal([]byte(afterReSave.String), &afterReSaveMap); err != nil {
		t.Fatalf("parse re-save tool_data: %v", err)
	}
	if string(afterReSaveMap["claude_session_id"]) != `"def"` {
		t.Errorf("typed update lost: claude_session_id = %s", afterReSaveMap["claude_session_id"])
	}
	if v, ok := afterReSaveMap["clear_on_compact"]; !ok || string(v) != "false" {
		t.Errorf("regression: clear_on_compact wiped on re-save (got %q, present=%v)", v, ok)
	}
}

// TestSaveInstances_PreservesClearOnCompactExtra is the batch-save analog
// of TestSaveInstance_PreservesClearOnCompactExtra. SaveInstances has its
// own separate code path (transaction, INSERT OR REPLACE per row) that
// must independently preserve unknown tool_data keys.
func TestSaveInstances_PreservesClearOnCompactExtra(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	now := time.Now()
	a := &InstanceRow{
		ID: "a", Title: "a", Status: "running", Tool: "claude",
		CreatedAt: now, LastAccessed: now, GroupPath: "default",
		ToolData: json.RawMessage(`{"claude_session_id":"a-v1"}`),
	}
	b := &InstanceRow{
		ID: "b", Title: "b", Status: "running", Tool: "codex",
		CreatedAt: now, LastAccessed: now, GroupPath: "default",
		ToolData: json.RawMessage(`{"codex_session_id":"b-v1"}`),
	}
	if err := db.SaveInstances([]*InstanceRow{a, b}); err != nil {
		t.Fatalf("first SaveInstances failed: %v", err)
	}

	// Manual writes adding extras to both rows.
	for _, id := range []string{"a", "b"} {
		if _, err := db.DB().Exec(
			`UPDATE instances SET tool_data = json_set(tool_data, '$.clear_on_compact', json('false')) WHERE id = ?`,
			id,
		); err != nil {
			t.Fatalf("manual update %s: %v", id, err)
		}
	}

	// Re-save with typed-only blobs.
	a.ToolData = json.RawMessage(`{"claude_session_id":"a-v2"}`)
	b.ToolData = json.RawMessage(`{"codex_session_id":"b-v2"}`)
	if err := db.SaveInstances([]*InstanceRow{a, b}); err != nil {
		t.Fatalf("second SaveInstances failed: %v", err)
	}

	for _, id := range []string{"a", "b"} {
		var raw sql.NullString
		if err := db.DB().QueryRow("SELECT tool_data FROM instances WHERE id = ?", id).Scan(&raw); err != nil {
			t.Fatalf("read after re-save %s: %v", id, err)
		}
		var m map[string]json.RawMessage
		_ = json.Unmarshal([]byte(raw.String), &m)
		if v, ok := m["clear_on_compact"]; !ok || string(v) != "false" {
			t.Errorf("regression on %s: clear_on_compact wiped on batch re-save (got %q, present=%v)", id, v, ok)
		}
	}
}
