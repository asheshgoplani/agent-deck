package session_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestKimiHookLifecyclePreservesConfigAndOnlyRemovesOwnedCommands(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	original := []byte(`default_model = "kimi-code/k3"
telemetry = false

[providers."managed:kimi-code"]
type = "kimi"
base_url = "https://api.kimi.com/coding/v1"
api_key = "test-credential-sentinel"

[models."kimi-code/k3"]
provider = "managed:kimi-code"
model = "k3"
max_context_size = 1048576
capabilities = ["thinking", "tool_use"]

[[hooks]]
event = "PreToolUse"
command = "/opt/wrap agent-deck hook-handler --debug"
matcher = "Bash"
timeout = 5

[[hooks]]
event = "Notification"
command = "notify-send Kimi"
`)
	if err := os.WriteFile(configPath, original, 0o644); err != nil {
		t.Fatal(err)
	}

	installed, err := session.InjectKimiHooks(dir)
	if err != nil || !installed {
		t.Fatalf("first install = %v, %v", installed, err)
	}
	installed, err = session.InjectKimiHooks(dir)
	if err != nil || installed {
		t.Fatalf("second install = %v, %v; want idempotent no-op", installed, err)
	}
	if got := len(readKimiHookEntries(t, dir)); got != len(session.KimiHookEventsForInstall())+2 {
		t.Fatalf("idempotent install produced %d entries", got)
	}

	removed, err := session.RemoveKimiHooks(dir)
	if err != nil || !removed {
		t.Fatalf("RemoveKimiHooks = %v, %v", removed, err)
	}
	entries := readKimiHookEntries(t, dir)
	if len(entries) != 2 || entries[0].Command != "/opt/wrap agent-deck hook-handler --debug" || entries[1].Command != "notify-send Kimi" {
		t.Fatalf("uninstall did not preserve exactly the unrelated hooks: %+v", entries)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := toml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse preserved config: %v", err)
	}
	if cfg["default_model"] != "kimi-code/k3" || cfg["telemetry"] != false {
		t.Errorf("top-level settings changed: %+v", cfg)
	}
	providers, _ := cfg["providers"].(map[string]any)
	provider, _ := providers["managed:kimi-code"].(map[string]any)
	if provider["api_key"] != "test-credential-sentinel" || provider["base_url"] != "https://api.kimi.com/coding/v1" {
		t.Errorf("provider settings changed: %+v", provider)
	}
	models, _ := cfg["models"].(map[string]any)
	model, _ := models["kimi-code/k3"].(map[string]any)
	if model["max_context_size"] != int64(1048576) {
		t.Errorf("model settings changed: %+v", model)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("config permissions = %04o, want 0600", info.Mode().Perm())
	}
}
