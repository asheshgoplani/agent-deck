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
	merged := false
	for k, v := range oldMap {
		if known[k] {
			continue // typed schema is authoritative
		}
		if _, exists := newMap[k]; exists {
			continue // new explicitly set this key
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
