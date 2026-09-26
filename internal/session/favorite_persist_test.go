package session

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

func TestFavoriteToolDataRoundTrip(t *testing.T) {
	td := json.RawMessage(`{"claude_session_id":"abc","idle_timeout_secs":60}`)
	on := WriteFavoriteToToolData(td, true, false)
	if !ReadFavoriteFromToolData(on) {
		t.Fatalf("favorite not set: %s", on)
	}
	if ReadIdleTimeoutSecsFromToolData(on) != 60 {
		t.Fatalf("unrelated key lost: %s", on)
	}
	off := WriteFavoriteToToolData(on, false, true)
	if ReadFavoriteFromToolData(off) || !strings.Contains(string(off), `"favorite":false`) {
		t.Fatalf("clear must write an explicit false: %s", off)
	}
	// The explicit false survives the extras merge a later save runs.
	merged := statedb.MergeToolDataExtras(on, WriteFavoriteToToolData(json.RawMessage(`{}`), false, true))
	if ReadFavoriteFromToolData(merged) {
		t.Fatalf("unset reverted by the extras merge: %s", merged)
	}
	if got := WriteFavoriteToToolData(json.RawMessage(`{"a":1}`), false, false); strings.Contains(string(got), "favorite") {
		t.Fatalf("never-favourite row gained a key: %s", got)
	}
	if ReadFavoriteFromToolData(nil) {
		t.Fatal("legacy row reads as favourite")
	}
	var inst Instance
	if _, _, err := SetField(&inst, FieldFavorite, "true", nil); err != nil || !inst.Favorite {
		t.Fatalf("SetField favorite: %v %v", err, inst.Favorite)
	}
	if _, _, err := SetField(&inst, FieldFavorite, "false", nil); err != nil || inst.Favorite || !inst.favoriteCleared {
		t.Fatalf("SetField unset: %v %v %v", err, inst.Favorite, inst.favoriteCleared)
	}
	if _, _, err := SetField(&inst, FieldFavorite, "yes please", nil); err == nil {
		t.Fatal("invalid favorite accepted")
	}
}
