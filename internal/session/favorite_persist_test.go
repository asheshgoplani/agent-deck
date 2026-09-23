package session

import (
	"encoding/json"
	"testing"
)

func TestFavoriteToolDataRoundTrip(t *testing.T) {
	td := json.RawMessage(`{"claude_session_id":"abc","idle_timeout_secs":60}`)
	on := WriteFavoriteToToolData(td, true)
	if !ReadFavoriteFromToolData(on) {
		t.Fatalf("favorite not set: %s", on)
	}
	if ReadIdleTimeoutSecsFromToolData(on) != 60 {
		t.Fatalf("unrelated key lost: %s", on)
	}
	off := WriteFavoriteToToolData(on, false)
	if ReadFavoriteFromToolData(off) || string(off) == string(on) {
		t.Fatalf("favorite not cleared: %s", off)
	}
	if ReadFavoriteFromToolData(nil) {
		t.Fatal("legacy row reads as favourite")
	}
	var inst Instance
	if _, _, err := SetField(&inst, FieldFavorite, "true", nil); err != nil || !inst.Favorite {
		t.Fatalf("SetField favorite: %v %v", err, inst.Favorite)
	}
	if _, _, err := SetField(&inst, FieldFavorite, "yes please", nil); err == nil {
		t.Fatal("invalid favorite accepted")
	}
}
