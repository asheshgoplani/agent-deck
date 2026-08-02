package session

import (
	"os"
	"path/filepath"
	"testing"
)

// writeUserConfig writes config.toml under the test's XDG config home and
// resets the user-config cache so the next LoadUserConfig reads it fresh.
func writeUserConfig(t *testing.T, xdgConfigHome, contents string) {
	t.Helper()
	cfgDir := filepath.Join(xdgConfigHome, "agent-deck")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", cfgDir, err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile(config.toml): %v", err)
	}
	ClearUserConfigCache()
}

// TestLoadUserConfig_NonNumericConductorID is the regression guard for the bug
// where a single non-numeric conductor bot ID in config.toml poisoned the whole
// TOML parse. Because user_id/guild_id/channel_id were plain int64 fields, a
// value like `user_id = "not-a-number"` made toml.DecodeFile fail, which made
// LoadUserConfig error out and took down completely unrelated commands (the
// config here also declares a remote, mirroring how `remote list` broke).
//
// After the fix these IDs decode tolerantly: a malformed value degrades to 0
// (bridge treats it as unset) instead of failing the load, so unrelated config
// (the remote) still parses.
func TestLoadUserConfig_NonNumericConductorID(t *testing.T) {
	_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)

	const cfg = `
[remotes.example]
host = "user@host"

[conductor.telegram]
token = "dummy-token"
user_id = "not-a-number"

[conductor.discord]
bot_token = "dummy-token"
guild_id = "also-not-a-number"
channel_id = "nope"
user_id = "still-not-numeric"
`
	writeUserConfig(t, xdgConfigHome, cfg)

	config, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig must not fail on a non-numeric conductor ID: %v", err)
	}
	if _, ok := config.Remotes["example"]; !ok {
		t.Fatalf("unrelated [remotes.example] must still parse; got remotes: %v", config.Remotes)
	}

	if got := config.Conductor.Telegram.UserID; got != 0 {
		t.Errorf("telegram user_id: want degrade to 0, got %d", got)
	}
	if got := config.Conductor.Discord.GuildID; got != 0 {
		t.Errorf("discord guild_id: want degrade to 0, got %d", got)
	}
	if got := config.Conductor.Discord.ChannelID; got != 0 {
		t.Errorf("discord channel_id: want degrade to 0, got %d", got)
	}
	if got := config.Conductor.Discord.UserID; got != 0 {
		t.Errorf("discord user_id: want degrade to 0, got %d", got)
	}
}

// TestLoadUserConfig_ConductorIDForms verifies the tolerant decoder still
// accepts the well-formed shapes: a bare TOML integer and a quoted numeric
// string (which users routinely write for these large IDs).
func TestLoadUserConfig_ConductorIDForms(t *testing.T) {
	_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)

	const cfg = `
[conductor.telegram]
token = "dummy-token"
user_id = 12345

[conductor.discord]
bot_token = "dummy-token"
guild_id = "67890"
channel_id = 24680
user_id = "13579"
`
	writeUserConfig(t, xdgConfigHome, cfg)

	config, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}

	if got := config.Conductor.Telegram.UserID; got != 12345 {
		t.Errorf("telegram user_id (bare int): want 12345, got %d", got)
	}
	if got := config.Conductor.Discord.GuildID; got != 67890 {
		t.Errorf("discord guild_id (quoted numeric): want 67890, got %d", got)
	}
	if got := config.Conductor.Discord.ChannelID; got != 24680 {
		t.Errorf("discord channel_id (bare int): want 24680, got %d", got)
	}
	if got := config.Conductor.Discord.UserID; got != 13579 {
		t.Errorf("discord user_id (quoted numeric): want 13579, got %d", got)
	}
}
