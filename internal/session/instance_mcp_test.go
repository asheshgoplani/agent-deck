package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMCPLocalConfigPathForTool(t *testing.T) {
	proj := "/tmp/p"
	if p := MCPLocalConfigPathForTool("cursor", proj); p != filepath.Join(proj, ".cursor", "mcp.json") {
		t.Fatalf("cursor: got %q", p)
	}
	if p := MCPLocalConfigPathForTool("claude", proj); p != filepath.Join(proj, ".mcp.json") {
		t.Fatalf("claude: got %q", p)
	}
	if p := MCPLocalConfigPathForTool("gemini", proj); p != "" {
		t.Fatalf("gemini: want empty project-local path, got %q", p)
	}
	if p := MCPLocalConfigPathForTool("opencode", proj); p != filepath.Join(proj, "opencode.json") {
		t.Fatalf("opencode: got %q", p)
	}
	if MCPLocalConfigPathForTool("claude", "") != "" {
		t.Fatal("empty project path")
	}
}

func TestMCPInfoForLocalAttach_GeminiUsesProjectMcpJsonWalker(t *testing.T) {
	tmp := t.TempDir()
	sub := filepath.Join(tmp, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	mcpFile := filepath.Join(tmp, ".mcp.json")
	if err := os.WriteFile(mcpFile, []byte(`{"mcpServers":{"walked":{"command":"true"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	info := MCPInfoForLocalAttach("gemini", sub)
	if info == nil {
		t.Fatal("nil info")
	}
	locals := info.Local()
	if len(locals) != 1 || locals[0] != "walked" {
		t.Fatalf("locals = %#v", locals)
	}
}

func TestToolSupportsMCPManager(t *testing.T) {
	if !ToolSupportsMCPManager("claude") || !ToolSupportsMCPManager("gemini") || !ToolSupportsMCPManager("cursor") {
		t.Fatal("expected claude, gemini, cursor")
	}
	if !ToolSupportsMCPManager("opencode") {
		t.Fatal("expected opencode")
	}
	if ToolSupportsMCPManager("shell") || ToolSupportsMCPManager("") {
		t.Fatal("unexpected tool")
	}
}
