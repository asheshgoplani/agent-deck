package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Issue #3104: a global attach/detach must merge one entry into opencode.json's
// mcp map, never replace the map, and HTTP servers must carry configured headers.

const opencodeMergeSeed = `{
  "model": "x/y",
  "mcp": {
    "remote-keep": {"type": "remote", "url": "https://keep.invalid/mcp", "headers": {"Authorization": "Bearer keep"}, "enabled": true},
    "local-keep": {"type": "local", "command": ["node", "srv.js"], "environment": {"K": "v"}},
    "cat": {"type": "remote", "url": "https://old.invalid/mcp", "headers": {"X-Old": "1"}, "timeout": 5000}
  }
}`

func setupOpenCodeMergeFixture(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	opencodeMCPConfigDirOverride = tmp
	t.Cleanup(func() { opencodeMCPConfigDirOverride = "" })
	cfg := &UserConfig{MCPs: map[string]MCPDef{
		"cat": {URL: "https://cat.invalid/mcp", Transport: "http", Headers: map[string]string{"Authorization": "Bearer cat"}},
		"dog": {Command: "echo", Args: []string{"woof"}},
	}}
	t.Cleanup(resetUserConfigCache(t, cfg))
	path := filepath.Join(tmp, "opencode.json")
	if err := os.WriteFile(path, []byte(opencodeMergeSeed), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readOpenCodeMCPMap(t *testing.T, path string) (map[string]any, map[string]any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	mcp, _ := root["mcp"].(map[string]any)
	return root, mcp
}

func assertUnrelatedSurvive(t *testing.T, mcp map[string]any) {
	t.Helper()
	var seed struct {
		MCP map[string]any `json:"mcp"`
	}
	if err := json.Unmarshal([]byte(opencodeMergeSeed), &seed); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"remote-keep", "local-keep"} {
		if !reflect.DeepEqual(mcp[name], seed.MCP[name]) {
			t.Fatalf("unrelated entry %q changed: got %v want %v", name, mcp[name], seed.MCP[name])
		}
	}
}

func TestWriteOpenCodeGlobalMCP_AttachKeepsUnrelatedEntries(t *testing.T) {
	path := setupOpenCodeMergeFixture(t)
	// CLI passes the currently attached names plus the new one.
	if err := WriteOpenCodeGlobalMCP([]string{"remote-keep", "local-keep", "cat", "dog"}); err != nil {
		t.Fatal(err)
	}
	root, mcp := readOpenCodeMCPMap(t, path)
	if root["model"] != "x/y" {
		t.Fatalf("non-mcp key lost: %v", root)
	}
	assertUnrelatedSurvive(t, mcp)
	if mcp["dog"] == nil {
		t.Fatal("attached entry missing")
	}
}

func TestWriteOpenCodeGlobalMCP_AttachCarriesConfiguredHeaders(t *testing.T) {
	path := setupOpenCodeMergeFixture(t)
	if err := WriteOpenCodeGlobalMCP([]string{"remote-keep", "local-keep", "cat"}); err != nil {
		t.Fatal(err)
	}
	_, mcp := readOpenCodeMCPMap(t, path)
	cat, _ := mcp["cat"].(map[string]any)
	headers, _ := cat["headers"].(map[string]any)
	if headers["Authorization"] != "Bearer cat" {
		t.Fatalf("configured headers not written: %v", cat)
	}
	if cat["url"] != "https://cat.invalid/mcp" || cat["type"] != "remote" {
		t.Fatalf("url/type wrong: %v", cat)
	}
	if cat["timeout"] != float64(5000) {
		t.Fatalf("unrelated field of existing entry dropped: %v", cat)
	}
}

func TestWriteOpenCodeGlobalMCP_AttachKeepsExistingHeadersWhenNoneConfigured(t *testing.T) {
	path := setupOpenCodeMergeFixture(t)
	cfg := &UserConfig{MCPs: map[string]MCPDef{
		"cat": {URL: "https://cat.invalid/mcp", Transport: "http"},
	}}
	t.Cleanup(resetUserConfigCache(t, cfg))
	if err := WriteOpenCodeGlobalMCP([]string{"cat"}); err != nil {
		t.Fatal(err)
	}
	_, mcp := readOpenCodeMCPMap(t, path)
	cat, _ := mcp["cat"].(map[string]any)
	headers, _ := cat["headers"].(map[string]any)
	if headers["X-Old"] != "1" {
		t.Fatalf("existing headers dropped: %v", cat)
	}
}

func TestWriteOpenCodeGlobalMCP_DetachKeepsUnrelatedEntries(t *testing.T) {
	path := setupOpenCodeMergeFixture(t)
	if err := WriteOpenCodeGlobalMCP([]string{"remote-keep", "local-keep", "cat", "dog"}); err != nil {
		t.Fatal(err)
	}
	// Detach dog: CLI passes the remaining names.
	if err := WriteOpenCodeGlobalMCP([]string{"remote-keep", "local-keep", "cat"}); err != nil {
		t.Fatal(err)
	}
	_, mcp := readOpenCodeMCPMap(t, path)
	assertUnrelatedSurvive(t, mcp)
	if _, ok := mcp["dog"]; ok {
		t.Fatal("detached entry still present")
	}
	if mcp["cat"] == nil {
		t.Fatal("other attached entry lost on detach")
	}
}

func TestWriteOpenCodeGlobalMCP_NativeV2KeepsUnrelatedEntries(t *testing.T) {
	path := setupOpenCodeMergeFixture(t)
	if err := WriteOpenCodeGlobalMCP([]string{"cat"}, true); err != nil {
		t.Fatal(err)
	}
	_, mcp := readOpenCodeMCPMap(t, path)
	servers, _ := mcp["servers"].(map[string]any)
	if servers["remote-keep"] == nil || servers["local-keep"] == nil || servers["cat"] == nil {
		t.Fatalf("v2 write lost entries: %v", mcp)
	}
}

func TestWriteOpenCodeGlobalMCP_AttachTypeChangeDropsStaleFields(t *testing.T) {
	path := setupOpenCodeMergeFixture(t)
	// "cat" was remote in the seed; the catalog now defines it as stdio.
	cfg := &UserConfig{MCPs: map[string]MCPDef{
		"cat": {Command: "echo", Args: []string{"meow"}},
	}}
	t.Cleanup(resetUserConfigCache(t, cfg))
	if err := WriteOpenCodeGlobalMCP([]string{"remote-keep", "local-keep", "cat"}); err != nil {
		t.Fatal(err)
	}
	_, mcp := readOpenCodeMCPMap(t, path)
	assertUnrelatedSurvive(t, mcp)
	cat, _ := mcp["cat"].(map[string]any)
	if cat["type"] != "local" {
		t.Fatalf("type not updated: %v", cat)
	}
	for _, stale := range []string{"url", "headers"} {
		if _, ok := cat[stale]; ok {
			t.Fatalf("stale %q survived type change: %v", stale, cat)
		}
	}
	if cat["timeout"] != float64(5000) {
		t.Fatalf("timeout should carry over: %v", cat)
	}
}

func TestWriteOpenCodeGlobalMCP_LegacyServerNamedServers(t *testing.T) {
	path := setupOpenCodeMergeFixture(t)
	seed := `{"mcp": {"servers": {"type": "remote", "url": "https://named.invalid/mcp"}}}`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteOpenCodeGlobalMCP([]string{"cat"}); err != nil {
		t.Fatal(err)
	}
	_, mcp := readOpenCodeMCPMap(t, path)
	named, _ := mcp["servers"].(map[string]any)
	if named["type"] != "remote" || named["url"] != "https://named.invalid/mcp" || len(named) != 2 {
		t.Fatalf("legacy server named \"servers\" changed: %v", mcp)
	}
	if mcp["cat"] == nil {
		t.Fatalf("attached entry not written beside the legacy server: %v", mcp)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg opencodeConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.MCP["cat"]; !ok {
		t.Fatalf("reader does not see the attached entry: %v", cfg.MCP)
	}
}
