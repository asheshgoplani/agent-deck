package session_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestWriteKanbanTaskIDToToolData_RoundTrip(t *testing.T) {
	var td json.RawMessage
	td = session.WriteKanbanTaskIDToToolData(td, "t_abc123")
	got := session.ReadKanbanTaskIDFromToolData(td)
	if got != "t_abc123" {
		t.Errorf("got %q, want %q", got, "t_abc123")
	}
}

func TestWriteKanbanTaskIDToToolData_Overwrites(t *testing.T) {
	td := session.WriteKanbanTaskIDToToolData(nil, "t_first")
	td = session.WriteKanbanTaskIDToToolData(td, "t_second")
	if got := session.ReadKanbanTaskIDFromToolData(td); got != "t_second" {
		t.Errorf("got %q, want %q", got, "t_second")
	}
}

func TestWriteKanbanTaskIDToToolData_EmptyRemovesKey(t *testing.T) {
	td := session.WriteKanbanTaskIDToToolData(nil, "t_abc123")
	td = session.WriteKanbanTaskIDToToolData(td, "")
	if got := session.ReadKanbanTaskIDFromToolData(td); got != "" {
		t.Errorf("expected empty after removal, got %q", got)
	}
	// The key must be absent from the JSON, not just set to "".
	var m map[string]json.RawMessage
	_ = json.Unmarshal(td, &m)
	if _, ok := m["kanban_task_id"]; ok {
		t.Error("kanban_task_id key still present after removal")
	}
}

func TestWriteKanbanTaskIDToToolData_CorruptedJSONPreservesOriginal(t *testing.T) {
	// Corrupted blob: not valid JSON. Before the fix, this would silently create
	// a fresh map and lose the existing tool_data. The fix returns the original.
	corrupted := json.RawMessage(`{not valid json`)
	result := session.WriteKanbanTaskIDToToolData(corrupted, "t_new")
	if !bytes.Equal(result, corrupted) {
		t.Errorf("corrupted input was not preserved: got %s, want %s", result, corrupted)
	}
}

func TestWriteKanbanTaskIDToToolData_PreservesOtherKeys(t *testing.T) {
	initial, _ := json.Marshal(map[string]string{"other_key": "other_value"})
	td := session.WriteKanbanTaskIDToToolData(initial, "t_abc")

	var m map[string]json.RawMessage
	if err := json.Unmarshal(td, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m["other_key"]; !ok {
		t.Error("other_key was lost after write")
	}
}

func TestReadKanbanTaskIDFromToolData_Empty(t *testing.T) {
	if got := session.ReadKanbanTaskIDFromToolData(nil); got != "" {
		t.Errorf("nil input: got %q, want empty", got)
	}
	if got := session.ReadKanbanTaskIDFromToolData(json.RawMessage{}); got != "" {
		t.Errorf("empty input: got %q, want empty", got)
	}
}

func TestReadKanbanTaskIDFromToolData_Malformed(t *testing.T) {
	// Malformed JSON: must return "" not panic.
	if got := session.ReadKanbanTaskIDFromToolData(json.RawMessage(`{bad}`)); got != "" {
		t.Errorf("malformed input: got %q, want empty", got)
	}
}
