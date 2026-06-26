package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestGetCodexMCPInfo(t *testing.T) {
	tmp := t.TempDir()
	configFile := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(configFile, []byte(`
[mcp_servers.zeta]
command = "echo"
args = ["z"]

[mcp_servers.alpha]
url = "https://example.com/mcp"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	ClearCodexMCPCache(tmp)
	info := GetCodexMCPInfo(tmp)
	if info == nil {
		t.Fatal("nil info")
	}
	if got, want := strings.Join(info.Global, ","), "alpha,zeta"; got != want {
		t.Fatalf("global MCPs = %q, want %q", got, want)
	}
	if len(info.LocalMCPs) != 0 || len(info.Project) != 0 {
		t.Fatalf("Codex MCPs should be global only: %#v", info)
	}
}

func TestWriteCodexMCPConfig_PreservesOtherKeys(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CODEX_HOME", tmp)

	cfg := &UserConfig{MCPs: map[string]MCPDef{
		"cat": {
			Command: "echo",
			Args:    []string{"purr"},
			Env:     map[string]string{"CAT": "meow"},
		},
		"web": {
			URL:     "https://example.com/mcp",
			Headers: map[string]string{"X-Test": "ok"},
		},
	}}
	restoreCfg := resetUserConfigCache(t, cfg)
	t.Cleanup(restoreCfg)

	configFile := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(configFile, []byte(`model = "gpt-5"

[profiles.fast]
model = "gpt-5-mini"

[mcp_servers.orphan]
command = "true"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := WriteCodexMCPConfig(tmp, []string{"cat", "web"}); err != nil {
		t.Fatal(err)
	}

	var raw map[string]interface{}
	if _, err := toml.DecodeFile(configFile, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["model"] != "gpt-5" {
		t.Fatalf("model not preserved: %#v", raw["model"])
	}
	if raw["profiles"] == nil {
		t.Fatal("profiles table not preserved")
	}

	var cfgOut codexMCPConfig
	if _, err := toml.DecodeFile(configFile, &cfgOut); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfgOut.MCPServers["orphan"]; ok {
		t.Fatal("orphan MCP should be replaced by managed set")
	}
	cat := cfgOut.MCPServers["cat"]
	if cat.Command != "echo" || strings.Join(cat.Args, ",") != "purr" || cat.Env["CAT"] != "meow" {
		t.Fatalf("cat config = %#v", cat)
	}
	web := cfgOut.MCPServers["web"]
	if web.URL != "https://example.com/mcp" || web.HTTPHeaders["X-Test"] != "ok" {
		t.Fatalf("web config = %#v", web)
	}
}

func TestCodexMCPDispatch(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CODEX_HOME", tmp)

	cfg := &UserConfig{
		Tools: map[string]ToolDef{
			"my-codex": {Command: "codex-wrapper", CompatibleWith: "codex"},
		},
		MCPs: map[string]MCPDef{
			"cat": {Command: "echo", Args: []string{"purr"}},
		},
	}
	restoreCfg := resetUserConfigCache(t, cfg)
	t.Cleanup(restoreCfg)

	if !ToolSupportsMCPManager("codex") {
		t.Fatal("codex should support MCP manager")
	}
	if !ToolSupportsMCPManager("my-codex") {
		t.Fatal("codex-compatible custom tool should support MCP manager")
	}
	if p := MCPLocalConfigPathForTool("codex", "/tmp/project"); p != "" {
		t.Fatalf("Codex has no project-local MCP path, got %q", p)
	}
	if p := MCPGlobalConfigPathForTool("codex"); p != filepath.Join(tmp, "config.toml") {
		t.Fatalf("Codex global path = %q, want config.toml under CODEX_HOME", p)
	}

	inst := NewInstanceWithTool("cx", "/tmp/project", "codex")
	if err := inst.WriteLocalMCPConfig([]string{"cat"}); err != nil {
		t.Fatal(err)
	}
	info := inst.GetMCPInfo()
	if got, want := strings.Join(info.Global, ","), "cat"; got != want {
		t.Fatalf("instance MCP info = %q, want %q", got, want)
	}
}
