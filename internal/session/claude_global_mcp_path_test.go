package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// #3103: with CLAUDE_CONFIG_DIR unset and no config_dir configured, Claude
// Code reads user-scope MCP servers from ~/.claude.json, not
// ~/.claude/.claude.json. Global attach/list/display must use that file.

func defaultClaudeDirEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if err := os.Unsetenv("CLAUDE_CONFIG_DIR"); err != nil {
		t.Fatal(err)
	}
	cfgDir := filepath.Join(home, ".config", "agent-deck")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("[mcps.alpha]\ncommand = \"alpha-server\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	if len(GetAvailableMCPs()) == 0 {
		t.Fatal("catalog did not load from the temp config")
	}
	return home
}

func TestWriteGlobalMCP_DefaultDirWritesHomeClaudeJSON_PreservesUnrelated(t *testing.T) {
	home := defaultClaudeDirEnv(t)
	root := filepath.Join(home, ".claude.json")
	seed := `{"numStartups": 7, "theme": "dark", "mcpServers": {"mine": {"type": "stdio", "command": "keep-me"}}, "projects": {"/x": {"allowedTools": ["a"]}}}`
	if err := os.WriteFile(root, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := WriteGlobalMCP([]string{"alpha"}); err != nil {
		t.Fatalf("WriteGlobalMCP: %v", err)
	}

	data, err := os.ReadFile(root)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"numStartups", "theme", "projects"} {
		if _, ok := got[k]; !ok {
			t.Errorf("unrelated top-level key %q dropped from ~/.claude.json", k)
		}
	}
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(got["mcpServers"], &servers); err != nil {
		t.Fatal(err)
	}
	if _, ok := servers["mine"]; !ok {
		t.Errorf("unrelated mcpServers entry %q was lost", "mine")
	}
	if _, ok := servers["alpha"]; !ok {
		t.Errorf("attached server missing from ~/.claude.json: %s", data)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", ".claude.json")); err == nil {
		t.Errorf("wrote ~/.claude/.claude.json, which Claude Code does not read")
	}
	names := GetGlobalMCPNames()
	if len(names) != 2 {
		t.Errorf("GetGlobalMCPNames = %v, want [alpha mine]", names)
	}
}

func TestMCPGlobalConfigPathForTool_Claude(t *testing.T) {
	home := defaultClaudeDirEnv(t)
	if got, want := MCPGlobalConfigPathForTool("claude"), filepath.Join(home, ".claude.json"); got != want {
		t.Errorf("default dir path = %q, want %q", got, want)
	}
	explicit := filepath.Join(home, ".claude-work")
	t.Setenv("CLAUDE_CONFIG_DIR", explicit)
	if got, want := MCPGlobalConfigPathForTool("claude"), filepath.Join(explicit, ".claude.json"); got != want {
		t.Errorf("explicit dir path = %q, want %q", got, want)
	}
}

// Reading must agree with writing: after a global attach in the default case,
// GetMCPInfo (used by `mcp attached`, `session show`, web and TUI) lists it,
// along with project MCPs from the projects map of ~/.claude.json.
func TestGetMCPInfo_ReadsHomeClaudeJSONAfterAttach(t *testing.T) {
	home := defaultClaudeDirEnv(t)
	proj := filepath.Join(home, "proj")
	seed := `{"projects": {"` + proj + `": {"mcpServers": {"pm": {"type": "stdio", "command": "x"}}}}}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteGlobalMCP([]string{"alpha"}); err != nil {
		t.Fatalf("WriteGlobalMCP: %v", err)
	}
	ClearMCPCache(proj)
	info := GetMCPInfo(proj)
	if len(info.Global) != 1 || info.Global[0] != "alpha" {
		t.Errorf("Global = %v, want [alpha]", info.Global)
	}
	if len(info.Project) != 1 || info.Project[0] != "pm" {
		t.Errorf("Project = %v, want [pm]", info.Project)
	}
}

// A config_dir set in config.toml is an explicit dir: use $DIR/.claude.json.
func TestClaudeConfigFilePath_ConfigTomlDir(t *testing.T) {
	home := defaultClaudeDirEnv(t)
	dir := filepath.Join(home, ".claude-cfg")
	toml := "[claude]\nconfig_dir = \"" + dir + "\"\n\n[mcps.alpha]\ncommand = \"alpha-server\"\n"
	if err := os.WriteFile(filepath.Join(home, ".config", "agent-deck", "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()
	if got, want := claudeConfigFilePath(), filepath.Join(dir, ".claude.json"); got != want {
		t.Errorf("config.toml dir path = %q, want %q", got, want)
	}
}
