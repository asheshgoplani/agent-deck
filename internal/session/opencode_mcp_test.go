package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGetOpenCodeConfigDir(t *testing.T) {
	opencodeMCPConfigDirOverride = ""
	dir := GetOpenCodeConfigDir()
	if dir == "" {
		t.Fatal("GetOpenCodeConfigDir returned empty string")
	}
	// Should end with ".config/opencode"
	if filepath.Base(dir) != "opencode" {
		t.Fatalf("expected last component 'opencode', got %q", filepath.Base(dir))
	}
}

func TestMCPLocalConfigPathForTool_Opencode(t *testing.T) {
	proj := "/tmp/ocp"
	got := MCPLocalConfigPathForTool("opencode", proj)
	want := filepath.Join(proj, "opencode.json")
	if got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
	if MCPLocalConfigPathForTool("opencode", "") != "" {
		t.Fatal("empty project path should return empty string")
	}
}

func TestMCPGlobalConfigPathForTool_Opencode(t *testing.T) {
	tmp := t.TempDir()
	opencodeMCPConfigDirOverride = tmp
	defer func() { opencodeMCPConfigDirOverride = "" }()

	got := MCPGlobalConfigPathForTool("opencode")
	want := filepath.Join(tmp, "opencode.json")
	if got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

func TestToolSupportsMCPManager_Opencode(t *testing.T) {
	if !ToolSupportsMCPManager("opencode") {
		t.Fatal("expected ToolSupportsMCPManager(\"opencode\") == true")
	}
}

func TestGetOpenCodeMCPInfo_GlobalAndLocal(t *testing.T) {
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "config", "opencode")
	proj := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}

	opencodeMCPConfigDirOverride = globalDir
	defer func() { opencodeMCPConfigDirOverride = "" }()

	globalJSON := `{"mcp":{"g1":{"type":"local","command":["echo","g"]},"g2":{"type":"remote","url":"http://x"}}}`
	if err := os.WriteFile(filepath.Join(globalDir, "opencode.json"), []byte(globalJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	localJSON := `{"mcp":{"l1":{"type":"local","command":["echo","l"]}}}`
	if err := os.WriteFile(filepath.Join(proj, "opencode.json"), []byte(localJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	ClearOpenCodeMCPCache(proj)
	info := GetOpenCodeMCPInfo(proj)
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

func TestGetOpenCodeMCPInfo_NoFiles(t *testing.T) {
	tmp := t.TempDir()
	opencodeMCPConfigDirOverride = tmp
	defer func() { opencodeMCPConfigDirOverride = "" }()

	info := GetOpenCodeMCPInfo("/nonexistent/project")
	if info == nil {
		t.Fatal("expected non-nil MCPInfo")
	}
	if len(info.Global) != 0 || len(info.LocalMCPs) != 0 {
		t.Fatalf("expected empty info, got %+v", info)
	}
}

func TestWriteOpenCodeProjectMCP_StdioFormat(t *testing.T) {
	tmp := t.TempDir()
	proj := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	opencodeMCPConfigDirOverride = tmp
	defer func() { opencodeMCPConfigDirOverride = "" }()

	cfg := &UserConfig{MCPs: map[string]MCPDef{
		"mytool": {Command: "npx", Args: []string{"-y", "mytool-mcp"}},
	}}
	restoreCfg := resetUserConfigCache(t, cfg)
	t.Cleanup(restoreCfg)

	if err := WriteOpenCodeProjectMCP(proj, []string{"mytool"}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(proj, "opencode.json"))
	if err != nil {
		t.Fatal(err)
	}

	var out opencodeConfig
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	srv, ok := out.MCP["mytool"]
	if !ok {
		t.Fatal("expected 'mytool' in mcp map")
	}
	if srv.Type != "local" {
		t.Fatalf("type = %q, want 'local'", srv.Type)
	}
	if len(srv.Command) == 0 || srv.Command[0] != "npx" {
		t.Fatalf("command = %v, want npx ...", srv.Command)
	}
}

func TestWriteOpenCodeProjectMCP_HTTPFormat(t *testing.T) {
	tmp := t.TempDir()
	proj := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	opencodeMCPConfigDirOverride = tmp
	defer func() { opencodeMCPConfigDirOverride = "" }()

	cfg := &UserConfig{MCPs: map[string]MCPDef{
		"remote-tool": {URL: "http://localhost:9999/mcp"},
	}}
	restoreCfg := resetUserConfigCache(t, cfg)
	t.Cleanup(restoreCfg)

	if err := WriteOpenCodeProjectMCP(proj, []string{"remote-tool"}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(proj, "opencode.json"))
	if err != nil {
		t.Fatal(err)
	}

	var out opencodeConfig
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	srv, ok := out.MCP["remote-tool"]
	if !ok {
		t.Fatal("expected 'remote-tool' in mcp map")
	}
	if srv.Type != "remote" {
		t.Fatalf("type = %q, want 'remote'", srv.Type)
	}
	if srv.URL != "http://localhost:9999/mcp" {
		t.Fatalf("url = %q, want 'http://localhost:9999/mcp'", srv.URL)
	}
}

func TestWriteOpenCodeProjectMCP_PreservesOtherKeys(t *testing.T) {
	tmp := t.TempDir()
	proj := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	opencodeMCPConfigDirOverride = tmp
	defer func() { opencodeMCPConfigDirOverride = "" }()

	cfg := &UserConfig{MCPs: map[string]MCPDef{
		"cat": {Command: "echo", Args: []string{"meow"}},
	}}
	restoreCfg := resetUserConfigCache(t, cfg)
	t.Cleanup(restoreCfg)

	seed := `{"theme":"dark","mcp":{"orphan":{"type":"local","command":["true"]}}}`
	mcpFile := filepath.Join(proj, "opencode.json")
	if err := os.WriteFile(mcpFile, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WriteOpenCodeProjectMCP(proj, []string{"cat"}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(mcpFile)
	if err != nil {
		t.Fatal(err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["theme"] != "dark" {
		t.Fatal("expected 'theme' key preserved")
	}
	mcpMap, ok := raw["mcp"].(map[string]interface{})
	if !ok {
		t.Fatal("mcp key missing or wrong type")
	}
	// cat should be present (from catalog), orphan is replaced
	if _, hasCat := mcpMap["cat"]; !hasCat {
		t.Fatal("expected 'cat' in mcp after write")
	}
}

func TestWriteOpenCodeGlobalMCP_PreservesOtherKeys(t *testing.T) {
	tmp := t.TempDir()
	opencodeMCPConfigDirOverride = tmp
	defer func() { opencodeMCPConfigDirOverride = "" }()

	cfg := &UserConfig{MCPs: map[string]MCPDef{
		"cat": {Command: "echo", Args: []string{"purr"}},
	}}
	restoreCfg := resetUserConfigCache(t, cfg)
	t.Cleanup(restoreCfg)

	path := filepath.Join(tmp, "opencode.json")
	seed := []byte(`{"foo":1,"mcp":{}}`)
	if err := os.WriteFile(path, seed, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WriteOpenCodeGlobalMCP([]string{"cat"}); err != nil {
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
		t.Fatal("expected 'foo' key preserved in global opencode.json")
	}
}

// Regression: an existing-but-unparseable opencode.json must NOT be
// overwritten. Earlier the parse-error branch reset rawConfig to an empty map
// and wrote it back, destroying every non-mcp key (model, theme, keybinds). The
// write must now fail closed and leave the file byte-for-byte untouched.
func TestWriteOpenCodeProjectMCP_RefusesOverwriteOfUnparseableConfig(t *testing.T) {
	tmp := t.TempDir()
	proj := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	opencodeMCPConfigDirOverride = tmp
	defer func() { opencodeMCPConfigDirOverride = "" }()

	cfg := &UserConfig{MCPs: map[string]MCPDef{
		"cat": {Command: "echo", Args: []string{"meow"}},
	}}
	restoreCfg := resetUserConfigCache(t, cfg)
	t.Cleanup(restoreCfg)

	// Garbage / partially-written JSON that still holds the user's real config.
	garbage := []byte(`{"theme":"dark","model":"opus",`) // truncated, unparseable
	mcpFile := filepath.Join(proj, "opencode.json")
	if err := os.WriteFile(mcpFile, garbage, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WriteOpenCodeProjectMCP(proj, []string{"cat"}); err == nil {
		t.Fatal("expected error when overwriting unparseable project config, got nil")
	}

	after, err := os.ReadFile(mcpFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(garbage) {
		t.Fatalf("unparseable project config was modified:\nbefore: %s\nafter:  %s", garbage, after)
	}
}

func TestWriteOpenCodeGlobalMCP_RefusesOverwriteOfUnparseableConfig(t *testing.T) {
	tmp := t.TempDir()
	opencodeMCPConfigDirOverride = tmp
	defer func() { opencodeMCPConfigDirOverride = "" }()

	cfg := &UserConfig{MCPs: map[string]MCPDef{
		"cat": {Command: "echo", Args: []string{"purr"}},
	}}
	restoreCfg := resetUserConfigCache(t, cfg)
	t.Cleanup(restoreCfg)

	path := filepath.Join(tmp, "opencode.json")
	garbage := []byte(`{"foo":1, this is not json`)
	if err := os.WriteFile(path, garbage, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WriteOpenCodeGlobalMCP([]string{"cat"}); err == nil {
		t.Fatal("expected error when overwriting unparseable global config, got nil")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(garbage) {
		t.Fatalf("unparseable global config was modified:\nbefore: %s\nafter:  %s", garbage, after)
	}
}

func TestOpenCodeRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	proj := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	opencodeMCPConfigDirOverride = tmp
	defer func() { opencodeMCPConfigDirOverride = "" }()

	cfg := &UserConfig{MCPs: map[string]MCPDef{
		"alpha": {Command: "alpha-bin", Args: []string{"--flag"}},
		"beta":  {Command: "beta-bin"},
	}}
	restoreCfg := resetUserConfigCache(t, cfg)
	t.Cleanup(restoreCfg)

	if err := WriteOpenCodeProjectMCP(proj, []string{"alpha", "beta"}); err != nil {
		t.Fatal(err)
	}

	ClearOpenCodeMCPCache(proj)
	info := GetOpenCodeMCPInfo(proj)
	names := make(map[string]bool)
	for _, lm := range info.LocalMCPs {
		names[lm.Name] = true
	}
	if !names["alpha"] || !names["beta"] {
		t.Fatalf("round-trip: got %v, want alpha and beta", info.LocalMCPs)
	}
}
