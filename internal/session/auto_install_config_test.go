package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

// Revert pin: checking by default must never imply installation by default.
func TestUpdateSettingsAutoInstallDefaultFalse(t *testing.T) {
	var cfg UserConfig
	if _, err := toml.Decode("[updates]\ncheck_enabled = true\n", &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Updates.GetAutoInstall() {
		t.Fatal("updates.auto_install must remain false when absent")
	}
}

func TestUpdateSettingsAutoInstallExplicitOptIn(t *testing.T) {
	var cfg UserConfig
	if _, err := toml.Decode("[updates]\nauto_install = true\n", &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Updates.GetAutoInstall() {
		t.Fatal("explicit updates.auto_install=true was not wired")
	}
}

func TestUpdateSettingsMigratesV1AutoUpdateOptIn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	isolateConfigHomeXDG(t)
	path, err := GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[updates]\nauto_update = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Updates.GetAutoInstall() {
		t.Fatal("v1 auto_update=true opt-in was lost during v2 migration")
	}
	if cfg.Updates.AutoUpdate != nil {
		t.Fatal("legacy v1 field was not cleared after migration")
	}
}

func TestUpdateSettingsV2ExplicitFalseOverridesV1True(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	isolateConfigHomeXDG(t)
	path, err := GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[updates]\nauto_update = true\nauto_install = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Updates.GetAutoInstall() {
		t.Fatal("explicit v2 auto_install=false must override legacy state")
	}
}
