package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// setupRemoteListJSONTest isolates config/cache to a temp HOME, like
// setupAddDefaultPathTest, and writes one configured remote "lab".
func setupRemoteListJSONTest(t *testing.T) {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	configDir := filepath.Join(home, ".config", "agent-deck")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	content := "[remotes.lab]\nhost = \"alice@lab.example\"\nagent_deck_path = \"/usr/local/bin/agent-deck\"\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
}

type remoteListJSONRow struct {
	Name             string `json:"name"`
	Host             string `json:"host"`
	AgentDeckPath    string `json:"agent_deck_path"`
	Profile          string `json:"profile"`
	Version          string `json:"version,omitempty"`
	VersionCheckedAt string `json:"version_checked_at,omitempty"`
	Outdated         bool   `json:"outdated"`
	VersionState     string `json:"version_state"`
}

// TestRemoteListJSON_VersionFields pins the `remote list --json` shape the
// remote preview panel's version line is built on: version, version_state
// (same/older/newer/unknown) and version_checked_at always present, even
// when the remote has never reported a version.
func TestRemoteVersionJSON_ListFields(t *testing.T) {
	orig := Version
	Version = "1.16.10"
	t.Cleanup(func() { Version = orig })

	t.Run("never checked reports unknown", func(t *testing.T) {
		setupRemoteListJSONTest(t)
		out := captureStdout(t, func() { handleRemoteList([]string{"--json"}) })

		var rows []remoteListJSONRow
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("unmarshal %q: %v", out, err)
		}
		if len(rows) != 1 {
			t.Fatalf("got %d rows, want 1: %+v", len(rows), rows)
		}
		row := rows[0]
		if row.Name != "lab" || row.Host != "alice@lab.example" {
			t.Fatalf("row = %+v", row)
		}
		if row.VersionState != "unknown" {
			t.Errorf("version_state = %q, want %q", row.VersionState, "unknown")
		}
		if row.Version != "" {
			t.Errorf("version = %q, want empty (never checked)", row.Version)
		}
		if row.VersionCheckedAt != "" {
			t.Errorf("version_checked_at = %q, want empty (never checked)", row.VersionCheckedAt)
		}
		if row.Outdated {
			t.Error("outdated must be false when the version is unknown")
		}
	})

	cases := []struct {
		name         string
		reported     string
		wantState    string
		wantOutdated bool
	}{
		{"same", "1.16.10", "same", false},
		{"older", "1.16.9", "older", true},
		{"newer", "1.16.11", "newer", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupRemoteListJSONTest(t)
			checkedAt := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
			if err := session.RecordRemoteVersions(map[string]session.RemoteVersionState{
				"lab": {Version: tc.reported, Found: true, CheckedAt: checkedAt},
			}); err != nil {
				t.Fatalf("RecordRemoteVersions: %v", err)
			}

			out := captureStdout(t, func() { handleRemoteList([]string{"--json"}) })
			var rows []remoteListJSONRow
			if err := json.Unmarshal([]byte(out), &rows); err != nil {
				t.Fatalf("unmarshal %q: %v", out, err)
			}
			if len(rows) != 1 {
				t.Fatalf("got %d rows, want 1: %+v", len(rows), rows)
			}
			row := rows[0]
			if row.Version != tc.reported {
				t.Errorf("version = %q, want %q", row.Version, tc.reported)
			}
			if row.VersionState != tc.wantState {
				t.Errorf("version_state = %q, want %q", row.VersionState, tc.wantState)
			}
			if row.Outdated != tc.wantOutdated {
				t.Errorf("outdated = %v, want %v", row.Outdated, tc.wantOutdated)
			}
			if row.VersionCheckedAt == "" {
				t.Error("version_checked_at must be set once the remote has been checked")
			}
		})
	}
}
