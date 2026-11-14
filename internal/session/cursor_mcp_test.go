package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGetCursorMCPInfo(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	proj := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	globalJSON := `{"mcpServers":{"g1":{"command":"echo","args":["g"]},"extra":{"command":"x"}}}`
	if err := os.WriteFile(filepath.Join(home, ".cursor", "mcp.json"), []byte(globalJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	localJSON := `{"mcpServers":{"l1":{"command":"echo","args":["l"]}}}`
	if err := os.WriteFile(filepath.Join(proj, ".cursor", "mcp.json"), []byte(localJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	ClearCursorMCPCache(proj)
	info := GetCursorMCPInfo(proj)
	if info == nil {
		t.Fatal("nil info")
	}
	if len(info.Global) != 2 {
		t.Fatalf("global count = %d, want 2: %v", len(info.Global), info.Global)
	}
	if len(info.LocalMCPs) != 1 || info.LocalMCPs[0].Name != "l1" {
		t.Fatalf("local = %#v", info.LocalMCPs)
	}
}

func TestWriteCursorProjectMCP_MergedWithUnmanaged(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	proj := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &UserConfig{MCPs: map[string]MCPDef{
		"cat": {Command: "echo", Args: []string{"meow"}},
	}}
	restoreCfg := resetUserConfigCache(t, cfg)
	t.Cleanup(restoreCfg)

	seed := `{"mcpServers":{"orphan":{"command":"true"}}}`
	mcpFile := filepath.Join(proj, ".cursor", "mcp.json")
	if err := os.WriteFile(mcpFile, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WriteCursorProjectMCP(proj, []string{"cat"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(mcpFile)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if _, ok := out.MCPServers["orphan"]; !ok {
		t.Fatal("expected orphan preserved")
	}
	if _, ok := out.MCPServers["cat"]; !ok {
		t.Fatal("expected cat from catalog")
	}
}

func TestWriteCursorGlobalMCP_PreservesOtherKeys(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	cfg := &UserConfig{MCPs: map[string]MCPDef{
		"cat": {Command: "echo", Args: []string{"purr"}},
	}}
	restoreCfg := resetUserConfigCache(t, cfg)
	t.Cleanup(restoreCfg)

	path := filepath.Join(home, ".cursor", "mcp.json")
	seed := []byte(`{"foo":1,"mcpServers":{}}`)
	if err := os.WriteFile(path, seed, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WriteCursorGlobalMCP([]string{"cat"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["foo"] == nil {
		t.Fatal("expected foo key preserved")
	}
}

// ASK-102: an unparseable or null-root ~/.cursor/mcp.json must be refused, not
// replaced; and the user's own non-catalog servers must survive a rewrite.
func TestWriteCursorGlobalMCP_FailsClosedAndKeepsUserServers(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Cleanup(resetUserConfigCache(t, &UserConfig{MCPs: map[string]MCPDef{"cat": {Command: "echo"}}}))
	path := filepath.Join(home, ".cursor", "mcp.json")

	for _, seed := range []string{`{"foo":1,"mcpServers":{"mine":`, "null"} {
		if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
			t.Fatal(err)
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("seed %q: panicked: %v", seed, r)
				}
			}()
			if err := WriteCursorGlobalMCP([]string{"cat"}); err == nil {
				t.Errorf("seed %q: expected error, got nil", seed)
			}
		}()
		if after, _ := os.ReadFile(path); string(after) != seed {
			t.Errorf("seed %q: file was modified to %s", seed, after)
		}
	}

	seed := `{"other":1,"mcpServers":{"mine":{"command":"foo"},"cat":{"command":"old"}}}`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteCursorGlobalMCP(nil); err != nil {
		t.Fatalf("WriteCursorGlobalMCP: %v", err)
	}
	data, _ := os.ReadFile(path)
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	servers, _ := raw["mcpServers"].(map[string]interface{})
	if raw["other"] == nil || servers["mine"] == nil || servers["cat"] != nil {
		t.Errorf("want other kept, user server 'mine' kept, managed 'cat' dropped; got %s", data)
	}
}
