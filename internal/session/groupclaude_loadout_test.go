package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyConfiguredLoadoutClaudeGroupUsesHomeSkillsAndProjectMCP(t *testing.T) {
	home := withIsolatedHomeAndConfig(t, `
[claude]
config_dir = "~/.claude-shared"
[mcps.memory]
command = "echo"
[groups.work.claude]
skills = ["store/alpha"]
mcps = ["memory"]
`)
	setupLoadoutStore(t, home)
	project := t.TempDir()
	inst := NewInstanceWithGroupAndTool("claude-loadout", project, "work", "claude")

	if warnings := ApplyConfiguredLoadout(inst); len(warnings) != 0 {
		t.Fatalf("warnings=%v", warnings)
	}
	if warnings := ApplyConfiguredLoadout(inst); len(warnings) != 0 {
		t.Fatalf("repeat warnings=%v", warnings)
	}

	claudeHome := filepath.Join(home, ".claude-shared")
	if _, err := os.Stat(filepath.Join(claudeHome, "skills", "alpha", "SKILL.md")); err != nil {
		t.Fatalf("Claude home skill missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(claudeHome, ".agent-deck", "skills.toml")); err != nil {
		t.Fatalf("Claude home manifest missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, ".claude", "skills")); !os.IsNotExist(err) {
		t.Fatalf("declarative Claude skills created project targets: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, ".agent-deck", "skills.toml")); !os.IsNotExist(err) {
		t.Fatalf("declarative Claude skills created project manifest: %v", err)
	}

	mcpData, err := os.ReadFile(filepath.Join(project, ".mcp.json"))
	if err != nil {
		t.Fatalf("Claude MCP must remain project-scoped: %v", err)
	}
	if !strings.Contains(string(mcpData), "memory") {
		t.Fatalf("project MCP config missing memory: %s", mcpData)
	}
}

func TestApplyConfiguredLoadoutClaudeHomeSkillsDoNotRequireProjectPath(t *testing.T) {
	home := withIsolatedHomeAndConfig(t, `
[claude]
config_dir = "~/.claude-shared"
[groups.work.claude]
skills = ["store/alpha"]
`)
	setupLoadoutStore(t, home)
	inst := NewInstanceWithGroupAndTool("claude-loadout", "", "work", "claude")

	if warnings := ApplyConfiguredLoadout(inst); len(warnings) != 0 {
		t.Fatalf("warnings=%v", warnings)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude-shared", "skills", "alpha", "SKILL.md")); err != nil {
		t.Fatalf("Claude home skill missing: %v", err)
	}
}
