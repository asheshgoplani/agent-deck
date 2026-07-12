package statedb

import (
	"encoding/json"
	"reflect"
	"strings"
)

// MergeToolDataExtras preserves any keys in oldToolData that are not part of
// agent-deck's typed tool_data schema (the toolDataBlob fields in this
// package) and that are not already present in newToolData. It returns the
// merged JSON to write back to the instances table.
//
// Why this exists: agent-deck's save path (SaveInstances) builds a fresh
// tool_data blob from typed Instance fields and INSERT OR REPLACEs the row
// wholesale. Any externally-written keys not modeled by toolDataBlob are
// silently dropped on every save cycle. The user-set `clear_on_compact`
// flag is the canonical example: it has no agent-deck CLI surface, so it
// is set by direct SQLite UPDATE; without this merge, it survives at most
// until the next session lifecycle event.
//
// The function is conservative: typed-known keys are not touched (the new
// blob's value wins, including absence-by-omitempty), and new explicitly
// setting a key wins over the old value (no silent override of intended
// updates). Only keys that are completely unknown to the typed schema AND
// absent from the new blob are carried forward.
//
// EXCEPTION — sticky detected session-id keys (stickyToolDataKeys): the
// per-tool conversation ids (claude_session_id / gemini_session_id /
// opencode_session_id / codex_session_id and their *_detected_at) are
// write-once-per-conversation identity, populated asynchronously after a
// session starts. They are treated like extras for the ABSENCE case: when the
// new blob OMITS them (omitempty zero-value) but the old row HAS them, the old
// value is carried forward, so an unrelated full-table save whose in-memory
// snapshot simply has not detected the id yet can no longer silently wipe a
// live session's mapping (t-0133). A NON-EMPTY new value still wins (a real
// resume/fork that changes the id), and an EXPLICIT empty value present in the
// new blob (`"claude_session_id":""`) is honored as an intentional clear — only
// outright OMISSION is treated as "unaware writer, preserve".
func MergeToolDataExtras(oldToolData, newToolData json.RawMessage) json.RawMessage {
	if len(oldToolData) == 0 {
		return newToolData
	}
	if len(newToolData) == 0 {
		newToolData = json.RawMessage("{}")
	}

	var oldMap map[string]json.RawMessage
	if err := json.Unmarshal(oldToolData, &oldMap); err != nil {
		return newToolData // old is corrupt; cannot merge
	}
	if len(oldMap) == 0 {
		return newToolData
	}

	var newMap map[string]json.RawMessage
	if err := json.Unmarshal(newToolData, &newMap); err != nil {
		return newToolData // new is corrupt; nothing to merge into
	}
	if newMap == nil {
		newMap = make(map[string]json.RawMessage)
	}

	known := toolDataKnownKeys()
	sticky := stickyToolDataKeys()
	merged := false
	for k, v := range oldMap {
		if known[k] && !sticky[k] {
			continue // typed schema is authoritative (except sticky identity keys)
		}
		if _, exists := newMap[k]; exists {
			continue // new explicitly set this key (incl. explicit empty = intentional clear)
		}
		newMap[k] = v
		merged = true
	}

	if !merged {
		return newToolData
	}
	out, err := json.Marshal(newMap)
	if err != nil {
		return newToolData
	}
	return out
}

// stickyToolDataKeys returns the typed tool_data keys that MergeToolDataExtras
// preserves from the old row when the new blob OMITS them (rather than letting
// omitempty-absence clear them). These are the per-tool conversation ids and
// their detection timestamps: identity that is detected asynchronously and is
// write-once for the life of a conversation. Keeping them sticky prevents a
// concurrent full-table save (e.g. the rename hook's LoadWithGroups +
// SaveWithGroups) whose snapshot has not yet observed the id from wiping a live
// session's session-id mapping (t-0133). A non-empty new value, or an explicit
// empty present in the new blob, still wins — only outright omission preserves.
func stickyToolDataKeys() map[string]bool {
	return map[string]bool{
		"claude_session_id":    true,
		"claude_detected_at":   true,
		"gemini_session_id":    true,
		"gemini_detected_at":   true,
		"opencode_session_id":  true,
		"opencode_detected_at": true,
		"codex_session_id":     true,
		"codex_detected_at":    true,
	}
}

// toolDataKnownKeys returns the set of JSON keys that toolDataBlob explicitly
// models. Used by MergeToolDataExtras to distinguish agent-deck's
// authoritative schema from externally-managed extras.
func toolDataKnownKeys() map[string]bool {
	t := reflect.TypeOf(toolDataBlob{})
	keys := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		if comma := strings.Index(tag, ","); comma >= 0 {
			tag = tag[:comma]
		}
		if tag != "" {
			keys[tag] = true
		}
	}
	return keys
}
