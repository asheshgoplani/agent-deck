package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestResolveVoiceHome(t *testing.T) {
	rows := []voiceHomeRow{
		{ID: "a1000001-1700000001", Title: "conductor-ops", ConductorName: "ops"},
		{ID: "a1000002-1700000002", Title: "conductor-work", ConductorName: "work"},
		{ID: "a1000003-1700000003", Title: "garden-planner"},
		{ID: "a1000004-1700000004", Title: "twin"},
		{ID: "a1000005-1700000005", Title: "Twin"},
		{ID: "a1000006-1700000006", Title: "old-home", Archived: true},
	}
	cases := []struct {
		configured, profile string
		want                VoiceHome
	}{
		{"", "work", VoiceHome{ID: "a1000002-1700000002", Title: "conductor-work", Source: "default"}},
		{"", "personal", VoiceHome{ID: "a1000001-1700000001", Title: "conductor-ops", Source: "default"}},
		{"garden-planner", "work", VoiceHome{ID: "a1000003-1700000003", Title: "garden-planner", Source: "config"}},
		{"GARDEN-PLANNER", "work", VoiceHome{ID: "a1000003-1700000003", Title: "garden-planner", Source: "config"}},
		{"a1000003-1700000003", "work", VoiceHome{ID: "a1000003-1700000003", Title: "garden-planner", Source: "config"}},
		{"a1000003", "work", VoiceHome{ID: "a1000003-1700000003", Title: "garden-planner", Source: "config"}},
		{"twin", "work", VoiceHome{ID: "a1000002-1700000002", Title: "conductor-work", Source: "default", Error: "ambiguous"}},
		{"old-home", "work", VoiceHome{ID: "a1000002-1700000002", Title: "conductor-work", Source: "default", Error: "not found"}},
		{"nowhere", "work", VoiceHome{ID: "a1000002-1700000002", Title: "conductor-work", Source: "default", Error: "not found"}},
	}
	for _, c := range cases {
		if got := resolveVoiceHome(c.configured, c.profile, rows); got != c.want {
			t.Errorf("home %q (profile %s) = %+v, want %+v", c.configured, c.profile, got, c.want)
		}
	}
	if got := resolveVoiceHome("", "work", nil); got != (VoiceHome{Source: "none"}) {
		t.Errorf("no sessions: %+v", got)
	}
}

func TestVoiceHomeJSONAndStateField(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("CLAUDE_VOICE_STATE_DIR", t.TempDir())
	code, out, _ := runVoiceT(t, "home", "--json")
	var h map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &h) != nil {
		t.Fatalf("code=%d out=%q", code, out)
	}
	for _, k := range []string{"id", "title", "host", "source"} {
		if _, ok := h[k]; !ok {
			t.Errorf("voice home lacks %q: %v", k, h)
		}
	}
	code, out, _ = runVoiceT(t, "state", "--json")
	var st map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &st) != nil {
		t.Fatalf("state code=%d out=%q", code, out)
	}
	if _, ok := st["home"].(map[string]any); !ok {
		t.Fatalf("voice state has no home object: %v", st)
	}
}
