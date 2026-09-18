// Context-level (issue #2260) persistence.
//
// Follows the identity_injection_persist.go / idle_timeout_persist.go
// pattern: the per-session override lives in the tool_data extras zone,
// outside the positional MarshalToolData / UnmarshalToolData signatures and
// without a SQL schema migration, so binaries that predate the key preserve
// it via MergeToolDataExtras. The key is deleted at the zero value (unset):
// an empty override means "inherit from group/global", and every legacy row
// (no key at all) means the same thing.
package session

import "encoding/json"

const toolDataContextLevelKey = "context_level"

// WriteContextLevelToToolData merges the per-session context-level override
// into the given tool_data blob, removing the key when level is empty.
// Sibling keys are preserved.
func WriteContextLevelToToolData(td json.RawMessage, level string) json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}
	if level == "" {
		delete(m, toolDataContextLevelKey)
	} else {
		raw, err := json.Marshal(level)
		if err != nil {
			return td
		}
		m[toolDataContextLevelKey] = raw
	}
	out, err := json.Marshal(m)
	if err != nil {
		return td
	}
	return out
}

// ReadContextLevelFromToolData extracts the per-session context-level
// override from the blob. Returns "" (unset/inherit) for missing,
// malformed, or legacy rows.
func ReadContextLevelFromToolData(td json.RawMessage) string {
	if len(td) == 0 {
		return ""
	}
	var blob struct {
		ContextLevel string `json:"context_level"`
	}
	_ = json.Unmarshal(td, &blob)
	return blob.ContextLevel
}
