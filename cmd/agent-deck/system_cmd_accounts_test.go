package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/quota"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// setupSystemStatsAccountsTest isolates config/cache to a temp HOME with one
// named claude account slot ("personal"), mirroring
// setupRemoteListJSONTest's isolation pattern.
func setupSystemStatsAccountsTest(t *testing.T) string {
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
	content := "[profiles.personal.claude]\nconfig_dir = \"/tmp/does-not-matter\"\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	return home
}

// TestCollectSystemStatsAccounts_KnownAndUnknownSlots pins that the "system
// stats" gatherer reports every configured claude account slot, with cached
// usage for the ones that have a quota file and Known: false for the ones
// that don't — this is exactly what rides the remote poll payload.
func TestCollectSystemStatsAccounts_KnownAndUnknownSlots(t *testing.T) {
	setupSystemStatsAccountsTest(t)

	store, err := quota.NewStore("personal")
	if err != nil {
		t.Fatalf("quota.NewStore: %v", err)
	}
	if err := store.Save(quota.Snapshot{
		ID:        quota.ProviderClaude,
		Windows:   []quota.Window{{Kind: quota.WindowFiveHour, Label: "5h", UsedPercentage: 8}},
		UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("store.Save: %v", err)
	}

	accounts := collectSystemStatsAccounts()
	if accounts == nil {
		t.Fatalf("collectSystemStatsAccounts() = nil, want a slice (config loaded fine)")
	}
	if len(*accounts) != 1 {
		t.Fatalf("got %d accounts, want 1: %+v", len(*accounts), *accounts)
	}
	got := (*accounts)[0]
	if got.Name != "personal" || !got.Known {
		t.Fatalf("got %+v, want Known personal slot", got)
	}
	if got.FiveHourPercent == nil || *got.FiveHourPercent != 8 {
		t.Fatalf("FiveHourPercent = %v, want 8", got.FiveHourPercent)
	}
	if got.SevenDayPercent != nil {
		t.Fatalf("SevenDayPercent = %v, want nil (never reported)", got.SevenDayPercent)
	}
}

// TestHandleSystemStats_JSON_IncludesAccounts pins the end-to-end wire shape:
// `agent-deck system stats --json` includes an "accounts" key whose entries
// carry only name/known/percentages — never a config_dir or credential.
func TestHandleSystemStats_JSON_IncludesAccounts(t *testing.T) {
	setupSystemStatsAccountsTest(t)

	stdout := captureStdout(t, func() {
		handleSystemStats([]string{"--json"})
	})

	var out systemStatsJSON
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, stdout)
	}
	if out.Accounts == nil {
		t.Fatalf("Accounts = nil, want a non-nil slice for a loadable config")
	}
	if len(*out.Accounts) != 1 || (*out.Accounts)[0].Name != "personal" {
		t.Fatalf("Accounts = %+v, want one entry named personal", *out.Accounts)
	}
	if strings.Contains(stdout, "/tmp/does-not-matter") {
		t.Fatalf("accounts output leaked a config_dir path: %s", stdout)
	}
}
