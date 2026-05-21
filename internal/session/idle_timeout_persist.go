// Issue #1143: idle-timeout JSON helpers.
//
// These thin wrappers merge / extract the idle_timeout_secs field on the
// tool_data blob without changing the positional MarshalToolData /
// UnmarshalToolData signatures. The MergeToolDataExtras layer in statedb
// preserves keys outside the typed schema across INSERT OR REPLACE, so a
// row written by an old binary survives a round-trip through a new binary
// (and vice versa).
package session

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const toolDataIdleTimeoutKey = "idle_timeout_secs"

// WriteIdleTimeoutSecsToToolData merges idle_timeout_secs into the given
// tool_data JSON blob. Passing secs == 0 removes the key (keeps the blob
// shape identical to a pre-#1143 row, so downgrades stay clean).
func WriteIdleTimeoutSecsToToolData(td json.RawMessage, secs int64) json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}
	if secs > 0 {
		raw, _ := json.Marshal(secs)
		m[toolDataIdleTimeoutKey] = raw
	} else {
		delete(m, toolDataIdleTimeoutKey)
	}
	out, _ := json.Marshal(m)
	return out
}

// ReadIdleTimeoutSecsFromToolData extracts idle_timeout_secs from the blob.
// Returns 0 (disabled) for missing/malformed/legacy rows.
func ReadIdleTimeoutSecsFromToolData(td json.RawMessage) int64 {
	if len(td) == 0 {
		return 0
	}
	var blob struct {
		IdleTimeoutSecs int64 `json:"idle_timeout_secs"`
	}
	_ = json.Unmarshal(td, &blob)
	return blob.IdleTimeoutSecs
}

// readFileOrEmpty returns file content; empty bytes + nil error when the file
// is missing. Used by tests to inspect the lifecycle log without flapping on
// the "no log written yet" path.
func readFileOrEmpty(path string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}
