package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSessionFavoriteRoundTrip: set, list, show, unset (macapp-core-needs,
// Favourites). The flag survives a fresh process (it is persisted).
func TestSessionFavoriteRoundTrip(t *testing.T) {
	home := t.TempDir()
	id := addSessionJSON(t, home, "fav-me", "claude")
	other := addSessionJSON(t, home, "not-fav", "claude")
	if stdout, stderr, code := runAgentDeck(t, home, "session", "set", id, "favorite", "true", "--json"); code != 0 {
		t.Fatalf("set favorite: %d %s %s", code, stdout, stderr)
	}
	stdout, _, _ := runAgentDeck(t, home, "session", "show", id, "--json")
	var show struct {
		Favorite bool `json:"favorite"`
	}
	if json.Unmarshal([]byte(stdout), &show) != nil || !show.Favorite {
		t.Fatalf("show after set: %s", stdout)
	}
	stdout, _, _ = runAgentDeck(t, home, "list", "--json")
	var list []struct {
		ID       string `json:"id"`
		Favorite bool   `json:"favorite"`
	}
	if err := json.Unmarshal([]byte(stdout), &list); err != nil {
		t.Fatalf("list JSON: %v %s", err, stdout)
	}
	favs := map[string]bool{}
	for _, s := range list {
		favs[s.ID] = s.Favorite
	}
	if !favs[id] || favs[other] {
		t.Fatalf("list favorites: %v", favs)
	}
	if _, _, code := runAgentDeck(t, home, "session", "set", id, "favorite", "maybe"); code == 0 {
		t.Fatal("invalid favorite accepted")
	}
	if _, _, code := runAgentDeck(t, home, "session", "set", id, "favorite", "false"); code != 0 {
		t.Fatalf("unset favorite: exit %d", code)
	}
	stdout, _, _ = runAgentDeck(t, home, "session", "show", id, "--json")
	if strings.Contains(stdout, `"favorite"`) {
		t.Fatalf("favorite still shown after unset: %s", stdout)
	}
}
