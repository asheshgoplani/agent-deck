package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyConfiguredLoadout_CodexGroupUsesAgentsSkillsAndGroupHomeMCP(t *testing.T) {
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

	if _, err := os.Stat(filepath.Join(project, ".agents", "skills", "alpha", "SKILL.md")); err != nil {
		t.Fatalf("expected Codex skill in .agents/skills: %v", err)
	}

	configPath := filepath.Join(home, ".codex-work", "config.toml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read group Codex config: %v", err)
	}
	if !strings.Contains(string(data), "[mcp_servers.memory]") {
		t.Errorf("group Codex MCP missing from config:\n%s", data)
	}
}
