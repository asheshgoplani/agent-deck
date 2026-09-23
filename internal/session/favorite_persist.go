package session

import "encoding/json"

// toolDataFavoriteKey stores the Favourites flag (macapp-core-needs,
// Favourites) in the tool_data extras zone, like idle_timeout_secs: no
// schema change, and a legacy binary preserves the key on save.
const toolDataFavoriteKey = "favorite"

// WriteFavoriteToToolData records the favorite flag in the blob. A plain
// false removes the key, so rows that were never favourites keep their bytes.
// cleared writes an explicit false instead: MergeToolDataExtras carries an
// absent key forward from the stored row, so a bare removal would silently
// revert an unset (same protocol as the generic session id clear).
func WriteFavoriteToToolData(td json.RawMessage, favorite, cleared bool) json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}
	switch {
	case favorite:
		m[toolDataFavoriteKey] = json.RawMessage("true")
	case cleared:
		m[toolDataFavoriteKey] = json.RawMessage("false")
	default:
		delete(m, toolDataFavoriteKey)
	}
	out, _ := json.Marshal(m)
	return out
}

// ReadFavoriteFromToolData reports the favorite flag; false for legacy rows.
func ReadFavoriteFromToolData(td json.RawMessage) bool {
	if len(td) == 0 {
		return false
	}
	var blob struct {
		Favorite bool `json:"favorite"`
	}
	_ = json.Unmarshal(td, &blob)
	return blob.Favorite
}
