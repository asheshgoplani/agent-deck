package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

// Issue #2552: an HTTP MCP that needs OAuth parameters (Claude Code's
// `claude mcp add --client-id ...`) must be expressible in config.toml and
// reach the generated Claude MCP config as the "oauth" object.
const issue2552ConfigTOML = `
[mcps.work]
url = "https://mcp.example.com/mcp"
transport = "http"
description = "My description"
[mcps.work.oauth]
  client_id = "client-123"
  callback_port = 8765
  auth_server_metadata_url = "https://auth.example.com/.well-known/oauth-authorization-server"
  scopes = "read write"

[mcps.plain]
url = "https://plain.example.com/mcp"
`

func loadIssue2552Config(t *testing.T) *UserConfig {
	t.Helper()
	var cfg UserConfig
	md, err := toml.Decode(issue2552ConfigTOML, &cfg)
	if err != nil {
		t.Fatalf("decode config.toml: %v", err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		t.Errorf("config.toml keys ignored by agent-deck: %v", undecoded)
	}
	return &cfg
}

func assertIssue2552OAuth(t *testing.T, where string, raw json.RawMessage) {
	t.Helper()
	var entry map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("%s: parse entry: %v", where, err)
	}
	oauthRaw, ok := entry["oauth"]
	if !ok {
		t.Fatalf("%s: mcpServers.work has no \"oauth\" object: %s", where, raw)
	}
	var oauth map[string]any
	if err := json.Unmarshal(oauthRaw, &oauth); err != nil {
		t.Fatalf("%s: parse oauth: %v", where, err)
	}
	want := map[string]any{
		"clientId":              "client-123",
		"callbackPort":          float64(8765),
		"authServerMetadataUrl": "https://auth.example.com/.well-known/oauth-authorization-server",
		"scopes":                "read write",
	}
	if len(oauth) != len(want) {
		t.Errorf("%s: oauth = %v, want exactly %v", where, oauth, want)
	}
	for k, v := range want {
		if oauth[k] != v {
			t.Errorf("%s: oauth[%q] = %v, want %v", where, k, oauth[k], v)
		}
	}
}

func TestIssue2552_MCPOAuthWrittenToProjectMCPJSON(t *testing.T) {
	t.Cleanup(resetUserConfigCache(t, loadIssue2552Config(t)))

	dir := t.TempDir()
	if err := WriteMCPJsonFromConfig(dir, []string{"work", "plain"}); err != nil {
		t.Fatalf("WriteMCPJsonFromConfig: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse .mcp.json: %v", err)
	}
	assertIssue2552OAuth(t, ".mcp.json", doc.MCPServers["work"])

	var plain map[string]json.RawMessage
	if err := json.Unmarshal(doc.MCPServers["plain"], &plain); err != nil {
		t.Fatal(err)
	}
	if _, ok := plain["oauth"]; ok {
		t.Errorf("MCP without [oauth] must not get an oauth key: %s", doc.MCPServers["plain"])
	}
}

func TestIssue2552_MCPOAuthInManagedServers(t *testing.T) {
	t.Cleanup(resetUserConfigCache(t, loadIssue2552Config(t)))

	servers := buildManagedMCPServers([]string{"work"}, "global")
	raw, err := json.Marshal(servers["work"])
	if err != nil {
		t.Fatal(err)
	}
	assertIssue2552OAuth(t, "managed", raw)
}

func TestIssue2552_MCPOAuthWrittenToUserScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Cleanup(resetUserConfigCache(t, loadIssue2552Config(t)))

	if err := WriteUserMCP([]string{"work"}); err != nil {
		t.Fatalf("WriteUserMCP: %v", err)
	}
	data, err := os.ReadFile(GetUserMCPRootPath())
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse user config: %v", err)
	}
	assertIssue2552OAuth(t, "user scope", doc.MCPServers["work"])
}
