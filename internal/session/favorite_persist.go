package session

import "encoding/json"

// toolDataFavoriteKey stores the Favourites flag (macapp-core-needs,
// Favourites) in the tool_data extras zone, like idle_timeout_secs: no
// schema change, and a legacy binary preserves the key on save.
const toolDataFavoriteKey = "favorite"

// WriteFavoriteToToolData sets or clears the favorite flag in the blob.
func WriteFavoriteToToolData(td json.RawMessage, favorite bool) json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}
	if favorite {
		m[toolDataFavoriteKey] = json.RawMessage("true")
	} else {
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
