// Custom-tool conversation identity across reboot.
//
// Built-in tools (claude/gemini/codex/…) persist *_session_id in tool_data so
// Restart can rebuild `<cmd> --resume <id>` after tmux dies. Custom tools
// configured via [tools.*] only stored the id in the live tmux environment
// (session_id_env), so a machine reboot left the deck row knowing the tool
// but not which conversation to resume.
//
// These helpers close that gap the same way last_started_at (#1704) and
// idle_timeout_secs (#1143) do: merge/extract keys on the tool_data JSON blob
// without extending the positional MarshalToolData signature. MergeToolDataExtras
// treats generic_session_id as sticky so a full-table save whose in-memory
// snapshot has not yet observed the id cannot wipe a live mapping.
package session

import (
	"encoding/json"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

const (
	toolDataGenericSessionIDKey  = "generic_session_id"
	toolDataGenericDetectedAtKey = "generic_detected_at"
)

// WriteGenericSessionIDToToolData merges generic_session_id (+ detected_at)
// into the given tool_data blob. An empty sessionID removes both keys so a
// clear is distinguishable from "never set".
func WriteGenericSessionIDToToolData(td json.RawMessage, sessionID string, detectedAt time.Time) json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}
	if sessionID == "" {
		delete(m, toolDataGenericSessionIDKey)
		delete(m, toolDataGenericDetectedAtKey)
	} else {
		rawID, _ := json.Marshal(sessionID)
		m[toolDataGenericSessionIDKey] = rawID
		if !detectedAt.IsZero() {
			rawAt, _ := json.Marshal(detectedAt.Unix())
			m[toolDataGenericDetectedAtKey] = rawAt
		}
	}
	out, _ := json.Marshal(m)
	return out
}

// ReadGenericSessionIDFromToolData extracts generic_session_id from the blob.
// Returns "" for missing/malformed/legacy rows.
func ReadGenericSessionIDFromToolData(td json.RawMessage) string {
	if len(td) == 0 {
		return ""
	}
	var blob struct {
		GenericSessionID string `json:"generic_session_id"`
	}
	_ = json.Unmarshal(td, &blob)
	return blob.GenericSessionID
}

// ReadGenericDetectedAtFromToolData extracts generic_detected_at (unix seconds).
func ReadGenericDetectedAtFromToolData(td json.RawMessage) time.Time {
	if len(td) == 0 {
		return time.Time{}
	}
	var blob struct {
		GenericDetectedAt int64 `json:"generic_detected_at"`
	}
	_ = json.Unmarshal(td, &blob)
	if blob.GenericDetectedAt == 0 {
		return time.Time{}
	}
	return time.Unix(blob.GenericDetectedAt, 0).UTC()
}

// persistGenericSessionIDIfChanged writes through to StateDB when the
// resolved custom-tool id differs from what is already on the instance.
// Safe no-op when statedb.GetGlobal() is nil or the id is empty.
func (i *Instance) persistGenericSessionIDIfChanged(sessionID string) {
	if i == nil || sessionID == "" {
		return
	}
	if i.GenericSessionID == sessionID {
		return
	}
	i.GenericSessionID = sessionID
	if i.GenericDetectedAt.IsZero() {
		i.GenericDetectedAt = time.Now()
	}
	if db := statedb.GetGlobal(); db != nil {
		_ = db.WriteGenericSessionBinding(i.ID, sessionID, i.GenericDetectedAt)
	}
}
