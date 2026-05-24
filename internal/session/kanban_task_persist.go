package session

import "encoding/json"

const toolDataKanbanTaskIDKey = "kanban_task_id"

// WriteKanbanTaskIDToToolData merges kanban_task_id into the tool_data JSON
// blob. Passing an empty string removes the key.
func WriteKanbanTaskIDToToolData(td json.RawMessage, taskID string) json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		if err := json.Unmarshal(td, &m); err != nil {
			return td
		}
	}
	if taskID != "" {
		raw, _ := json.Marshal(taskID)
		m[toolDataKanbanTaskIDKey] = raw
	} else {
		delete(m, toolDataKanbanTaskIDKey)
	}
	out, _ := json.Marshal(m)
	return out
}

// ReadKanbanTaskIDFromToolData extracts kanban_task_id from the blob.
// Returns "" for missing/malformed/legacy rows.
func ReadKanbanTaskIDFromToolData(td json.RawMessage) string {
	if len(td) == 0 {
		return ""
	}
	var blob struct {
		KanbanTaskID string `json:"kanban_task_id"`
	}
	_ = json.Unmarshal(td, &blob)
	return blob.KanbanTaskID
}
