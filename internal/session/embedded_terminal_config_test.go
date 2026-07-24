package session

import (
	"testing"

	"github.com/BurntSushi/toml"
)

func TestEmbeddedTerminalDefaultsOff(t *testing.T) {
	if (UISettings{}).GetEmbeddedTerminal() {
		t.Fatal("unset [ui].embedded_terminal should preserve the classic layout")
	}
}

func TestEmbeddedTerminalCanBeDisabled(t *testing.T) {
	var cfg UserConfig
	if _, err := toml.Decode("[ui]\nembedded_terminal = false\n", &cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if cfg.UI.GetEmbeddedTerminal() {
		t.Fatal("explicit embedded_terminal=false was ignored")
	}
}

func TestEmbeddedTerminalExplicitTrueStaysOn(t *testing.T) {
	var cfg UserConfig
	if _, err := toml.Decode("[ui]\nembedded_terminal = true\n", &cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if !cfg.UI.GetEmbeddedTerminal() {
		t.Fatal("explicit embedded_terminal=true was ignored")
	}
}

func TestMergePanelConfigPropagatesEmbeddedTerminalOff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	on := true
	if err := SaveUserConfig(&UserConfig{UI: UISettings{EmbeddedTerminal: &on}}); err != nil {
		t.Fatalf("save starting config: %v", err)
	}
	off := false
	merged, err := MergePanelConfigOntoDisk(&UserConfig{UI: UISettings{EmbeddedTerminal: &off}})
	if err != nil {
		t.Fatalf("merge settings panel config: %v", err)
	}
	if merged.UI.EmbeddedTerminal == nil || *merged.UI.EmbeddedTerminal {
		t.Fatal("settings merge dropped explicit embedded_terminal=false")
	}
}
