package session

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestCodexTUISettingsDecodeAndPreserveExplicitFalse(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[codex.tui]
status_line = ["model-with-reasoning", "context-used", "git-branch"]
status_line_use_colors = false
`)

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Codex.TUI == nil {
		t.Fatal("Codex TUI settings were not decoded")
	}
	want := []string{"model-with-reasoning", "context-used", "git-branch"}
	if !reflect.DeepEqual(cfg.Codex.TUI.StatusLine, want) {
		t.Fatalf("status_line=%v, want %v", cfg.Codex.TUI.StatusLine, want)
	}
	if cfg.Codex.TUI.StatusLineUseColors == nil || *cfg.Codex.TUI.StatusLineUseColors {
		t.Fatalf("status_line_use_colors=%v, want explicit false", cfg.Codex.TUI.StatusLineUseColors)
	}
}

func TestApplyCodexTUISettingsPreservesUnrelatedConfig(t *testing.T) {
	codexHome := t.TempDir()
	configPath := filepath.Join(codexHome, "config.toml")
	existing := `model = "gpt-5.6-sol"

[tui]
theme = "catppuccin-mocha"

[tui.model_availability_nux]
"gpt-5.6-sol" = 4

[mcp_servers.memory]
command = "memory-server"

[projects."/tmp/work"]
trust_level = "trusted"
`
	if err := os.WriteFile(configPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	useColors := true
	settings := &CodexTUISettings{
		StatusLine:          []string{"model-with-reasoning", "context-used", "git-branch"},
		StatusLineUseColors: &useColors,
	}

	if err := ApplyCodexTUISettings(codexHome, settings); err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if _, err := toml.DecodeFile(configPath, &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "gpt-5.6-sol" {
		t.Fatalf("model was not preserved: %#v", got["model"])
	}
	tui, ok := got["tui"].(map[string]any)
	if !ok {
		t.Fatalf("tui table missing: %#v", got["tui"])
	}
	if tui["theme"] != "catppuccin-mocha" {
		t.Fatalf("tui.theme was not preserved: %#v", tui)
	}
	if tui["status_line_use_colors"] != true {
		t.Fatalf("status_line_use_colors=%#v", tui["status_line_use_colors"])
	}
	if _, ok := tui["model_availability_nux"]; !ok {
		t.Fatalf("tui.model_availability_nux was not preserved: %#v", tui)
	}
	if _, ok := got["mcp_servers"]; !ok {
		t.Fatalf("mcp_servers were not preserved: %#v", got)
	}
	if _, ok := got["projects"]; !ok {
		t.Fatalf("projects were not preserved: %#v", got)
	}
}

func TestApplyCodexTUISettingsHonorsExplicitEmptyStatusLine(t *testing.T) {
	codexHome := t.TempDir()
	if err := ApplyCodexTUISettings(codexHome, &CodexTUISettings{StatusLine: []string{}}); err != nil {
		t.Fatal(err)
	}

	var got struct {
		TUI struct {
			StatusLine []string `toml:"status_line"`
		} `toml:"tui"`
	}
	if _, err := toml.DecodeFile(filepath.Join(codexHome, "config.toml"), &got); err != nil {
		t.Fatal(err)
	}
	if got.TUI.StatusLine == nil || len(got.TUI.StatusLine) != 0 {
		t.Fatalf("status_line=%#v, want explicit empty list", got.TUI.StatusLine)
	}
}

func TestApplyCodexTUISettingsRejectsUnsafeHome(t *testing.T) {
	traversingHome := t.TempDir() + string(os.PathSeparator) + "link" + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "home"
	for _, codexHome := range []string{"", ".", string(os.PathSeparator), traversingHome} {
		if err := ApplyCodexTUISettings(codexHome, &CodexTUISettings{StatusLine: []string{"model"}}); err == nil {
			t.Errorf("ApplyCodexTUISettings(%q) succeeded, want unsafe-home error", codexHome)
		}
	}
}

func TestApplyConfiguredLoadoutCodexTUIWithoutSkillsOrMCPs(t *testing.T) {
	home := withIsolatedHomeAndConfig(t, `
[codex.tui]
status_line = ["model", "current-dir"]

[groups.work.codex]
config_dir = "~/.codex-work"
`)
	t.Setenv("CODEX_HOME", "")
	inst := NewInstanceWithGroupAndTool("codex-tui", "", "work", "codex")

	if warnings := ApplyConfiguredLoadout(inst); len(warnings) != 0 {
		t.Fatalf("warnings=%v", warnings)
	}
	assertCodexStatusLine(t, filepath.Join(home, ".codex-work"), []string{"model", "current-dir"})
}

func TestSyncGroupCodexPluginsAppliesTUIWithoutPlugins(t *testing.T) {
	home := withIsolatedHomeAndConfig(t, `
[codex.tui]
status_line = ["model", "git-branch"]

[groups.work.codex]
config_dir = "~/.codex-work"
`)

	if err := SyncGroupCodexPlugins("work"); err != nil {
		t.Fatal(err)
	}
	assertCodexStatusLine(t, filepath.Join(home, ".codex-work"), []string{"model", "git-branch"})
}

func assertCodexStatusLine(t *testing.T, codexHome string, want []string) {
	t.Helper()
	var got struct {
		TUI struct {
			StatusLine []string `toml:"status_line"`
		} `toml:"tui"`
	}
	if _, err := toml.DecodeFile(filepath.Join(codexHome, "config.toml"), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.TUI.StatusLine, want) {
		t.Fatalf("status_line=%v, want %v", got.TUI.StatusLine, want)
	}
}
