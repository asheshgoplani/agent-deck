package session

import (
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
