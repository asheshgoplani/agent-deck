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
//
// Sticky vs intentional clear: omission of generic_session_id is treated as
// "unaware writer — preserve". A deliberate clear (SetField tool-session-id "",
// RestartFresh) must therefore either:
//  1. set Instance.genericSessionIDCleared so instanceToRow writes an EXPLICIT
//     empty string (sticky honors explicit empty), or
//  2. call WriteGenericSessionBinding("", …) (json_remove) before Save.
// Writing explicit empty on every empty GenericSessionID would break sticky
// protection for concurrent full-table saves — do not do that.
//
// The clear flag is one-shot: SaveWithGroups / InsertSessionAndVerify /
// PersistRecoveredInstances call consumeGenericSessionIDCleared after a
// successful DB write so a later unrelated full save cannot keep emitting
// explicit empty and wipe a concurrent re-bind. Do not clear the flag inside
// instanceToRow alone — a failed Upsert must still retry with intentional clear.
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
// into the given tool_data blob.
//
//   - Non-empty sessionID: writes the id (and detected_at when non-zero).
//   - Empty sessionID + intentionalClear: writes explicit "" / 0 so
//     MergeToolDataExtras honors the clear (sticky only preserves on omission).
//   - Empty sessionID + !intentionalClear: omits both keys so a stale full-table
//     save cannot wipe a binding written concurrently via WriteGenericSessionBinding.
func WriteGenericSessionIDToToolData(td json.RawMessage, sessionID string, detectedAt time.Time, intentionalClear bool) json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}
	if sessionID == "" {
		if intentionalClear {
			m[toolDataGenericSessionIDKey] = json.RawMessage(`""`)
			m[toolDataGenericDetectedAtKey] = json.RawMessage(`0`)
		} else {
			delete(m, toolDataGenericSessionIDKey)
			delete(m, toolDataGenericDetectedAtKey)
		}
	} else {
		rawID, _ := json.Marshal(sessionID)
		m[toolDataGenericSessionIDKey] = rawID
		if !detectedAt.IsZero() {
			rawAt, _ := json.Marshal(detectedAt.Unix())
			m[toolDataGenericDetectedAtKey] = rawAt
		} else {
			delete(m, toolDataGenericDetectedAtKey)
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
// Values are stored as Unix epoch seconds (timezone-independent); the returned
// time is UTC so Equal comparisons against time.Unix(...).UTC() round-trip.
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

// PersistGenericSessionBinding write-throughs generic_session_id to the given
// StateDB. Used by CLI `session set tool-session-id` which may not have
// registered statedb.SetGlobal. Empty GenericSessionID clears via json_remove;
// non-empty sets/updates. Defense in depth alongside genericSessionIDCleared +
// SaveWithGroups. Safe no-op when db or inst is nil.
func PersistGenericSessionBinding(db *statedb.StateDB, inst *Instance) error {
	if db == nil || inst == nil {
		return nil
	}
	return db.WriteGenericSessionBinding(inst.ID, inst.GenericSessionID, inst.GenericDetectedAt)
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
	i.genericSessionIDCleared = false
	if i.GenericDetectedAt.IsZero() {
		i.GenericDetectedAt = time.Now()
	}
	if db := statedb.GetGlobal(); db != nil {
		_ = db.WriteGenericSessionBinding(i.ID, sessionID, i.GenericDetectedAt)
	}
}
