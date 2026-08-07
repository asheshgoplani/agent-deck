package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyConfiguredLoadout_CodexHealsMissingNativePlugins(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, "codex-work")
	record := filepath.Join(home, "plugin-invocation.txt")
	fakeCodex := filepath.Join(home, "codex")
	script := "#!/bin/sh\nprintf '%s\\n' \"$CODEX_HOME $*\" >> " + record + "\n"
	if err := os.WriteFile(fakeCodex, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	withIsolatedHomeAndConfig(t, `
[groups."work".codex]
config_dir = "`+codexHome+`"
command = "`+fakeCodex+`"
marketplaces = ["team-marketplace"]
plugins = ["agent-deck@team"]
`)

	inst := NewInstanceWithGroupAndTool("codex-loadout", t.TempDir(), "work", "codex")
	if warnings := ApplyConfiguredLoadout(inst); len(warnings) != 0 {
		t.Fatalf("unexpected loadout warnings: %v", warnings)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("read plugin invocations: %v", err)
	}
	for _, fragment := range []string{
		"plugin marketplace add team-marketplace --json",
		"plugin add agent-deck@team --json",
	} {
		if !strings.Contains(string(data), fragment) {
			t.Errorf("missing %q in invocations:\n%s", fragment, data)
		}
	}
}

func TestGroupCodexPluginsHealthyRequiresEnabledPluginAndMarketplace(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write("[plugins.\"agent-deck@team\"]\nenabled = true\n")
	if healthy, err := groupCodexPluginsHealthy(configPath, []string{"agent-deck@team"}); err != nil || healthy {
		t.Fatalf("plugin without marketplace healthy=%v err=%v", healthy, err)
	}

	write("[marketplaces.team]\nsource = \"/tmp/team\"\n[plugins.\"agent-deck@team\"]\nenabled = false\n")
	if healthy, err := groupCodexPluginsHealthy(configPath, []string{"agent-deck@team"}); err != nil || healthy {
		t.Fatalf("disabled plugin healthy=%v err=%v", healthy, err)
	}

	write("[marketplaces.team]\nsource = \"/tmp/team\"\n[plugins.\"agent-deck@team\"]\nenabled = true\n")
	if healthy, err := groupCodexPluginsHealthy(configPath, []string{"agent-deck@team"}); err != nil || !healthy {
		t.Fatalf("complete plugin floor healthy=%v err=%v", healthy, err)
	}
}

func TestApplyConfiguredLoadout_CodexGroupUsesHomeSkillsAndGroupHomeMCP(t *testing.T) {
	home := withIsolatedHomeAndConfig(t, `
[mcps.memory]
command = "echo"

[groups."work".codex]
config_dir = "~/.codex-work"
skills = ["store/alpha"]
mcps = ["memory"]
`)
	t.Setenv("CODEX_HOME", "")

	store := t.TempDir()
	writeSkillDir(t, store, "alpha", "alpha", "test skill")
	if err := SaveSkillSources(map[string]SkillSourceDef{
		"store": {Path: store, Enabled: boolPtr(true)},
	}); err != nil {
		t.Fatalf("save skill source: %v", err)
	}

	project := t.TempDir()
	inst := NewInstanceWithGroupAndTool("codex-loadout", project, "work", "codex")
	if warnings := ApplyConfiguredLoadout(inst); len(warnings) != 0 {
		t.Fatalf("unexpected loadout warnings: %v", warnings)
	}
	if warnings := ApplyConfiguredLoadout(inst); len(warnings) != 0 {
		t.Fatalf("healthy home loadout was not a no-op: %v", warnings)
	}

	codexHome := filepath.Join(home, ".codex-work")
	if _, err := os.Stat(filepath.Join(codexHome, "skills", "alpha", "SKILL.md")); err != nil {
		t.Fatalf("expected Codex skill in CODEX_HOME/skills: %v", err)
	}
	for _, generated := range []string{".agents", ".agent-deck"} {
		if _, err := os.Stat(filepath.Join(project, generated)); !os.IsNotExist(err) {
			t.Fatalf("Codex group loadout created project state %s: %v", generated, err)
		}
	}

	configPath := filepath.Join(codexHome, "config.toml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read group Codex config: %v", err)
	}
	if !strings.Contains(string(data), "[mcp_servers.memory]") {
		t.Errorf("group Codex MCP missing from config:\n%s", data)
	}
}
