// Issue #1704 follow-up (dual-review blocker): Instance.LastStartedAt was
// declared as a "persisted stamp" (see the field's doc comment in
// instance.go) but was never actually wired into InstanceData or SQLite —
// it only ever lived in the in-process struct. Every out-of-process reader
// (status --stale, ShouldSkipRestart's freshness guard via a fresh CLI
// invocation) saw it as permanently zero.
//
// These helpers close that gap the same way #1143 added idle_timeout_secs:
// merge/extract a key on the tool_data JSON blob without touching the
// positional MarshalToolData/UnmarshalToolData signature or the SQL schema.
// The MergeToolDataExtras layer in statedb preserves the key across
// INSERT OR REPLACE, so a row written by a binary that predates this key
// round-trips cleanly (old binary just never sees/writes it).
package session

import (
	"encoding/json"
	"time"
)

const toolDataLastStartedAtKey = "last_started_at"

// WriteLastStartedAtToToolData merges last_started_at into the given
// tool_data JSON blob as a Unix-seconds integer. A zero time removes the key
// (keeps the blob shape identical to a pre-fix row, so a session that was
// added but genuinely never started stays indistinguishable from one saved
// by an older binary).
func WriteLastStartedAtToToolData(td json.RawMessage, t time.Time) json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}
	if !t.IsZero() {
		raw, _ := json.Marshal(t.Unix())
		m[toolDataLastStartedAtKey] = raw
	} else {
		delete(m, toolDataLastStartedAtKey)
	}
	out, _ := json.Marshal(m)
	return out
}

// ReadLastStartedAtFromToolData extracts last_started_at from the blob.
// Returns the zero time.Time for missing/malformed/legacy rows — callers
// must treat that as "unknown" (old record or genuinely never started), the
// same contract Instance.LastStartedAt's doc comment already establishes.
func ReadLastStartedAtFromToolData(td json.RawMessage) time.Time {
	if len(td) == 0 {
		return time.Time{}
	}
	var blob struct {
		LastStartedAt int64 `json:"last_started_at"`
	}
	_ = json.Unmarshal(td, &blob)
	if blob.LastStartedAt == 0 {
		return time.Time{}
	}
	return time.Unix(blob.LastStartedAt, 0).UTC()
}
