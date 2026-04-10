package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestLoadUserConfig_SystemStatsSection(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	err := os.WriteFile(configPath, []byte(`
[system_stats]
enabled = true
refresh_seconds = 10
format = "full"
show = ["cpu", "ram", "gpu"]
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	var cfg UserConfig
	if _, err := toml.DecodeFile(configPath, &cfg); err != nil {
		t.Fatalf("DecodeFile: %v", err)
	}

	if !cfg.SystemStats.GetEnabled() {
		t.Error("expected enabled = true")
	}
	if cfg.SystemStats.GetRefreshSeconds() != 10 {
		t.Errorf("refresh_seconds = %d, want 10", cfg.SystemStats.GetRefreshSeconds())
	}
	if cfg.SystemStats.GetFormat() != "full" {
		t.Errorf("format = %q, want %q", cfg.SystemStats.GetFormat(), "full")
	}
	show := cfg.SystemStats.GetShow()
	if len(show) != 3 || show[0] != "cpu" || show[1] != "ram" || show[2] != "gpu" {
		t.Errorf("show = %v, want [cpu ram gpu]", show)
	}
}

func TestLoadUserConfig_SystemStatsDisabled(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	disabled := false
	err := os.WriteFile(configPath, []byte(`
[system_stats]
enabled = false
`), 0644)
	if err != nil {
		t.Fatal(err)
	}
	_ = disabled

	var cfg UserConfig
	if _, err := toml.DecodeFile(configPath, &cfg); err != nil {
		t.Fatalf("DecodeFile: %v", err)
	}

	if cfg.SystemStats.GetEnabled() {
		t.Error("expected enabled = false")
	}
}

func TestSystemStatsSettings_Defaults(t *testing.T) {
	var cfg SystemStatsSettings

	if !cfg.GetEnabled() {
		t.Error("default enabled should be true")
	}
	if cfg.GetRefreshSeconds() != 5 {
		t.Errorf("default refresh = %d, want 5", cfg.GetRefreshSeconds())
	}
	if cfg.GetFormat() != "compact" {
		t.Errorf("default format = %q, want compact", cfg.GetFormat())
	}
	show := cfg.GetShow()
	if len(show) != 4 {
		t.Errorf("default show = %v, want [cpu ram disk network]", show)
	}
}

func TestSystemStatsSettings_RefreshClamping(t *testing.T) {
	tests := []struct {
		input int
		want  int
	}{
		{0, 5},   // below min -> default
		{1, 5},   // below min -> default
		{2, 2},   // at min
		{300, 300}, // at max
		{301, 300}, // above max -> capped
	}
	for _, tt := range tests {
		cfg := SystemStatsSettings{RefreshSeconds: tt.input}
		got := cfg.GetRefreshSeconds()
		if got != tt.want {
			t.Errorf("GetRefreshSeconds(%d) = %d, want %d", tt.input, got, tt.want)
		}
	}
}
