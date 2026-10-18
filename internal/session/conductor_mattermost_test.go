package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMattermostSettings_LoadAndSaveRoundTrip(t *testing.T) {
	tmpHome := setupConductorTest(t)
	writeConductorConfig(t, tmpHome, `
[conductor.mattermost]
server_url = "https://mattermost.example.com"
bot_token = "keychain:agent-deck-mattermost"
user = "mwallace"
channel_id = "abcdefghijklmnopqrstuvwxyz"
listen_mode = "mentions"
allow_insecure_http = true
`)
	want := MattermostSettings{
		ServerURL:         "https://mattermost.example.com",
		BotToken:          "keychain:agent-deck-mattermost",
		User:              "mwallace",
		ChannelID:         "abcdefghijklmnopqrstuvwxyz",
		ListenMode:        "mentions",
		AllowInsecureHTTP: true,
	}

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if cfg.Conductor.Mattermost != want {
		t.Fatalf("loaded %+v, want %+v", cfg.Conductor.Mattermost, want)
	}

	if err := SaveUserConfig(cfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	ClearUserConfigCache()
	reloaded, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Conductor.Mattermost != want {
		t.Fatalf("after save, loaded %+v, want %+v", reloaded.Conductor.Mattermost, want)
	}
}

func TestMattermostSettings_UnsetTableIsNotWritten(t *testing.T) {
	tmpHome := setupConductorTest(t)
	writeConductorConfig(t, tmpHome, "[conductor.slack]\nbot_token = \"xoxb-1\"\n")

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if err := SaveUserConfig(cfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	saved, err := os.ReadFile(filepath.Join(tmpHome, ".agent-deck", "config.toml"))
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if strings.Contains(string(saved), "mattermost") {
		t.Fatalf("saved config gained a mattermost table:\n%s", saved)
	}
}

func TestBridgeTemplate_ContainsMattermostBot(t *testing.T) {
	for _, pattern := range []string{
		"HAS_AIOHTTP",
		"class MattermostBridge:",
		"def create_mattermost_bot(config: dict)",
		`mm = conductor_cfg.get("mattermost", {})`,
		`mm_bot_token = _resolve_secret(mm.get("bot_token", ""))`,
		`"Mattermost", mattermost_bot.run,`,
		"Unauthorized Mattermost message from user",
	} {
		if !strings.Contains(conductorBridgePy, pattern) {
			t.Errorf("bridge should contain Mattermost pattern: %q", pattern)
		}
	}
}
