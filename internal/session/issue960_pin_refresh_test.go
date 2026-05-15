package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPluginUpgrade_RefreshesMcpJsonPins_RegressionFor960 covers issue #960:
// when a Claude Code plugin upgrades on disk, any .mcp.json entry whose
// command/args reference the now-stale <profile>/plugins/cache/<source>/<name>/<version>/
// path must be rewritten to the currently-installed version. Without this,
// /mcp reconnect silently keeps loading the outdated plugin.
func TestPluginUpgrade_RefreshesMcpJsonPins_RegressionFor960(t *testing.T) {
	profileDir := t.TempDir()
	projectDir := t.TempDir()

	source := "marketplace-x"
	name := "fooplug"
	pluginRoot := filepath.Join(profileDir, "plugins", "cache", source, name)

	// Install plugin v1.0.0 on disk.
	v1Dir := filepath.Join(pluginRoot, "1.0.0")
	if err := os.MkdirAll(v1Dir, 0o755); err != nil {
		t.Fatalf("mkdir v1: %v", err)
	}
	v1Server := filepath.Join(v1Dir, "server.js")
	if err := os.WriteFile(v1Server, []byte("v1"), 0o644); err != nil {
		t.Fatalf("write v1 server: %v", err)
	}

	// Write a .mcp.json entry that pins the v1 absolute path. The entry name
	// is NOT in agent-deck's config.toml catalog, so the existing
	// WriteMCPJsonFromConfig merge logic would preserve it verbatim (#146).
	mcpFile := filepath.Join(projectDir, ".mcp.json")
	initial := map[string]any{
		"mcpServers": map[string]any{
			name: map[string]any{
				"type":    "stdio",
				"command": "node",
				"args":    []string{v1Server, "--flag"},
			},
		},
	}
	initialBytes, err := json.MarshalIndent(initial, "", "  ")
	if err != nil {
		t.Fatalf("marshal initial: %v", err)
	}
	if err := os.WriteFile(mcpFile, initialBytes, 0o644); err != nil {
		t.Fatalf("write initial mcp.json: %v", err)
	}

	// Simulate plugin upgrade: 1.0.0 removed, 2.0.0 installed.
	if err := os.RemoveAll(v1Dir); err != nil {
		t.Fatalf("remove v1: %v", err)
	}
	v2Dir := filepath.Join(pluginRoot, "2.0.0")
	if err := os.MkdirAll(v2Dir, 0o755); err != nil {
		t.Fatalf("mkdir v2: %v", err)
	}
	v2Server := filepath.Join(v2Dir, "server.js")
	if err := os.WriteFile(v2Server, []byte("v2"), 0o644); err != nil {
		t.Fatalf("write v2 server: %v", err)
	}

	// Trigger refresh.
	refreshed, err := RefreshStalePluginPins(mcpFile, []string{profileDir})
	if err != nil {
		t.Fatalf("RefreshStalePluginPins: %v", err)
	}
	if refreshed != 1 {
		t.Errorf("expected 1 entry refreshed, got %d", refreshed)
	}

	// Verify .mcp.json now references v2 path, not v1.
	data, err := os.ReadFile(mcpFile)
	if err != nil {
		t.Fatalf("read mcp.json after refresh: %v", err)
	}
	s := string(data)
	if strings.Contains(s, "1.0.0") {
		t.Errorf(".mcp.json still references stale 1.0.0 pin after refresh:\n%s", s)
	}
	if !strings.Contains(s, v2Server) {
		t.Errorf(".mcp.json missing refreshed v2 path %q:\n%s", v2Server, s)
	}

	// The non-versioned --flag arg must survive untouched.
	if !strings.Contains(s, "--flag") {
		t.Errorf(".mcp.json lost --flag arg during refresh:\n%s", s)
	}
}
