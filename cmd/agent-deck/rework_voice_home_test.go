package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/voicedesk"
)

// M4 (a): with several conductors and none named exactly like the profile,
// home is his conductor whose name ends in the profile name; never another
// person's conductor; never an archived one.
func TestReworkVoiceHomeDefaultIsHisConductor(t *testing.T) {
	rows := []voiceHomeRow{
		{ID: "a", Title: "conductor-deck-aaron", ConductorName: "deck-aaron"},
		{ID: "b", Title: "conductor-deck-alex", ConductorName: "deck-alex"},
		{ID: "c", Title: "conductor-deck-zed", ConductorName: "deck-zed"},
	}
	if h := resolveVoiceHome("", "alex", rows); h.ID != "b" || h.Source != "default" {
		t.Errorf("profile alex, conductors deck-aaron/deck-alex/deck-zed: home = %+v, want deck-alex", h)
	}

	// Sam's conductor sorts first; it must never be home.
	cfg := voicedesk.Config{
		OwnerGroups: []string{"team/alex"},
		People:      map[string]voicedesk.Person{"sam": {Groups: []string{"team/sam"}}},
	}
	rows2 := []voiceHomeRow{
		{ID: "s", Title: "conductor-aaron", ConductorName: "aaron", Group: "team/sam"},
		{ID: "d", Title: "conductor-desk", ConductorName: "desk", Group: "team/alex", Archived: true},
		{ID: "z", Title: "conductor-zeta", ConductorName: "zeta", Group: "team/alex"},
	}
	if h := resolveVoiceHomeFor(cfg, "", "desk", rows2); h.ID != "z" {
		t.Errorf("desk archived, aaron is Sam's: home = %+v, want conductor-zeta", h)
	}
	// Only someone else's (or nobody's) conductor left: no home.
	if h := resolveVoiceHomeFor(cfg, "", "desk", rows2[:2]); h.ID != "" || h.Source != "none" {
		t.Errorf("only Sam's conductor live: home = %+v, want none", h)
	}
	nobody := []voiceHomeRow{{ID: "n", Title: "conductor-stray", ConductorName: "stray", Group: "misc"}}
	if h := resolveVoiceHomeFor(cfg, "", "desk", nobody); h.ID != "" {
		t.Errorf("nobody's conductor: home = %+v, want none", h)
	}
	// Exact profile name beats a suffix match.
	rows3 := []voiceHomeRow{
		{ID: "x", Title: "conductor-deck-work", ConductorName: "deck-work"},
		{ID: "y", Title: "conductor-work", ConductorName: "work"},
	}
	if h := resolveVoiceHome("", "work", rows3); h.ID != "y" {
		t.Errorf("exact name should win over suffix: %+v", h)
	}
}

// M4 (b): a configured home that is archived, renamed or missing falls back
// to the default and keeps the error.
func TestReworkVoiceHomeConfiguredGoneFallsBack(t *testing.T) {
	rows := []voiceHomeRow{
		{ID: "b", Title: "conductor-deck-alex", ConductorName: "deck-alex"},
		{ID: "o", Title: "old-home", Archived: true},
		{ID: "t1", Title: "twin"},
		{ID: "t2", Title: "Twin"},
	}
	for _, c := range []struct{ configured, why string }{
		{"gone-session", "not found"},
		{"old-home", "not found"},
		{"twin", "ambiguous"},
	} {
		h := resolveVoiceHome(c.configured, "alex", rows)
		if h.ID != "b" || h.Source != "default" || h.Error != c.why {
			t.Errorf("configured %q: home = %+v, want deck-alex (default) with error %q", c.configured, h, c.why)
		}
	}
	if h := resolveVoiceHome("gone", "alex", rows[1:]); h.ID != "" || h.Source != "none" || h.Error != "not found" {
		t.Errorf("configured gone, no conductor: %+v", h)
	}
}

// S6: `voice state --json` must not run the session list on every call.
func TestReworkVoiceStateDoesNotListEveryCall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_VOICE_STATE_DIR", t.TempDir())
	calls := 0
	old := voiceHomeList
	voiceHomeList = func(ctx context.Context, profile string) ([]voiceHomeRow, error) {
		calls++
		return []voiceHomeRow{{ID: "c1", Title: "conductor-default", ConductorName: "default"}}, nil
	}
	t.Cleanup(func() { voiceHomeList = old })

	for i := 0; i < 5; i++ {
		code, out, _ := runVoiceT(t, "state", "--json")
		var st map[string]any
		if code != 0 || json.Unmarshal([]byte(out), &st) != nil {
			t.Fatalf("state code=%d out=%q", code, out)
		}
		h, ok := st["home"].(map[string]any)
		if !ok || h["id"] != "c1" {
			t.Fatalf("voice state home = %v, want c1", st["home"])
		}
	}
	if calls > 1 {
		t.Errorf("voice state --json ran the session list %d times in 5 calls, want at most 1", calls)
	}
	// `voice home` always resolves fresh.
	before := calls
	if code, _, _ := runVoiceT(t, "home", "--json"); code != 0 || calls != before+1 {
		t.Errorf("voice home: code=%d, list calls %d -> %d, want one fresh list", code, before, calls)
	}
}
