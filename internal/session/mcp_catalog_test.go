package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteMCPJsonFromConfig(t *testing.T) {
	// Create temp directory for project
	tmpDir, err := os.MkdirTemp("", "mcp-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Test with empty enabled list
	err = WriteMCPJsonFromConfig(tmpDir, []string{})
	if err != nil {
		t.Fatalf("WriteMCPJsonFromConfig failed: %v", err)
	}

	// Verify .mcp.json was created
	mcpFile := filepath.Join(tmpDir, ".mcp.json")
	data, err := os.ReadFile(mcpFile)
	if err != nil {
		t.Fatalf("Failed to read .mcp.json: %v", err)
	}

	var mcpConfig struct {
		MCPServers map[string]MCPServerConfig `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &mcpConfig); err != nil {
		t.Fatalf("Failed to parse .mcp.json: %v", err)
	}

	if len(mcpConfig.MCPServers) != 0 {
		t.Errorf("Expected empty mcpServers, got %d", len(mcpConfig.MCPServers))
	}
}

func TestMCPServerConfigJSON(t *testing.T) {
	// Test that MCPServerConfig marshals correctly for stdio
	config := MCPServerConfig{
		Type:    "stdio",
		Command: "npx",
		Args:    []string{"-y", "test-mcp"},
		Env:     map[string]string{"API_KEY": "test123"},
	}

	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var parsed MCPServerConfig
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if parsed.Command != config.Command {
		t.Errorf("Command mismatch: got %q, want %q", parsed.Command, config.Command)
	}
	if len(parsed.Args) != len(config.Args) {
		t.Errorf("Args length mismatch: got %d, want %d", len(parsed.Args), len(config.Args))
	}
	if parsed.Env["API_KEY"] != config.Env["API_KEY"] {
		t.Errorf("Env mismatch: got %q, want %q", parsed.Env["API_KEY"], config.Env["API_KEY"])
	}
}

func TestMCPServerConfigHTTP(t *testing.T) {
	// Test that MCPServerConfig marshals correctly for HTTP transport
	config := MCPServerConfig{
		Type: "http",
		URL:  "https://mcp.example.com/mcp",
	}

	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var parsed MCPServerConfig
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if parsed.Type != "http" {
		t.Errorf("Type mismatch: got %q, want %q", parsed.Type, "http")
	}
	if parsed.URL != config.URL {
		t.Errorf("URL mismatch: got %q, want %q", parsed.URL, config.URL)
	}
	// Command should be empty for HTTP MCPs
	if parsed.Command != "" {
		t.Errorf("Command should be empty for HTTP MCP, got %q", parsed.Command)
	}
}

func TestMCPServerConfigSSE(t *testing.T) {
	// Test that MCPServerConfig marshals correctly for SSE transport
	config := MCPServerConfig{
		Type: "sse",
		URL:  "https://mcp.asana.com/sse",
	}

	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var parsed MCPServerConfig
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if parsed.Type != "sse" {
		t.Errorf("Type mismatch: got %q, want %q", parsed.Type, "sse")
	}
	if parsed.URL != config.URL {
		t.Errorf("URL mismatch: got %q, want %q", parsed.URL, config.URL)
	}
}

func TestGetGlobalMCPNames(t *testing.T) {
	// Create temp directory for Claude config
	tmpDir, err := os.MkdirTemp("", "claude-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Set CLAUDE_CONFIG_DIR to temp
	oldConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	os.Setenv("CLAUDE_CONFIG_DIR", tmpDir)
	defer func() {
		if oldConfigDir != "" {
			os.Setenv("CLAUDE_CONFIG_DIR", oldConfigDir)
		} else {
			os.Unsetenv("CLAUDE_CONFIG_DIR")
		}
	}()

	// Create a Claude config with MCPs
	config := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"exa": map[string]interface{}{
				"command": "npx",
				"args":    []string{"-y", "exa-mcp"},
			},
			"reddit": map[string]interface{}{
				"command": "npx",
				"args":    []string{"-y", "reddit-mcp"},
			},
		},
	}
	data, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(filepath.Join(tmpDir, ".claude.json"), data, 0600); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Test GetGlobalMCPNames
	names := GetGlobalMCPNames()
	if len(names) != 2 {
		t.Errorf("Expected 2 MCPs, got %d", len(names))
	}

	// Should be sorted
	if names[0] != "exa" || names[1] != "reddit" {
		t.Errorf("Expected [exa, reddit], got %v", names)
	}
}

func TestGetProjectMCPNames(t *testing.T) {
	// Create temp directory for Claude config
	tmpDir, err := os.MkdirTemp("", "claude-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Set CLAUDE_CONFIG_DIR to temp
	oldConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	os.Setenv("CLAUDE_CONFIG_DIR", tmpDir)
	defer func() {
		if oldConfigDir != "" {
			os.Setenv("CLAUDE_CONFIG_DIR", oldConfigDir)
		} else {
			os.Unsetenv("CLAUDE_CONFIG_DIR")
		}
	}()

	projectPath := "/test/my-project"

	// Create a Claude config with project-specific MCPs
	config := map[string]interface{}{
		"mcpServers": map[string]interface{}{},
		"projects": map[string]interface{}{
			projectPath: map[string]interface{}{
				"mcpServers": map[string]interface{}{
					"notion": map[string]interface{}{
						"command": "npx",
						"args":    []string{"-y", "notion-mcp"},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(filepath.Join(tmpDir, ".claude.json"), data, 0600); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Test GetProjectMCPNames
	names := GetProjectMCPNames(projectPath)
	if len(names) != 1 {
		t.Errorf("Expected 1 MCP, got %d", len(names))
	}
	if len(names) > 0 && names[0] != "notion" {
		t.Errorf("Expected [notion], got %v", names)
	}

	// Non-existent project should return nil
	names2 := GetProjectMCPNames("/other/path")
	if names2 != nil {
		t.Errorf("Expected nil for non-existent project, got %v", names2)
	}
}

func TestGetUserMCPRootPath(t *testing.T) {
	// GetUserMCPRootPath should return ~/.claude.json
	path := GetUserMCPRootPath()
	if path == "" {
		t.Fatal("GetUserMCPRootPath returned empty string")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("Failed to get home dir: %v", err)
	}

	expected := filepath.Join(home, ".claude.json")
	if path != expected {
		t.Errorf("Expected %q, got %q", expected, path)
	}
}

func TestGetUserMCPNames(t *testing.T) {
	// Create temp home dir
	tmpHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", originalHome)

	// Create ~/.claude.json with test MCPs
	claudeJSON := filepath.Join(tmpHome, ".claude.json")
	content := `{
		"mcpServers": {
			"exa": {"command": "npx", "args": ["-y", "exa-mcp-server"]},
			"reddit": {"command": "npx", "args": ["-y", "reddit-mcp-buddy"]}
		}
	}`
	if err := os.WriteFile(claudeJSON, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	// Test
	names := GetUserMCPNames()
	if len(names) != 2 {
		t.Errorf("expected 2 MCPs, got %d", len(names))
	}
	if len(names) >= 2 && (names[0] != "exa" || names[1] != "reddit") {
		t.Errorf("expected [exa, reddit], got %v", names)
	}
}

func TestGetUserMCPNamesNoFile(t *testing.T) {
	// Create temp home dir with no .claude.json
	tmpHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", originalHome)

	// Test - should return nil when file doesn't exist
	names := GetUserMCPNames()
	if names != nil {
		t.Errorf("expected nil when file doesn't exist, got %v", names)
	}
}

func TestGetUserMCPNamesEmptyServers(t *testing.T) {
	// Create temp home dir
	tmpHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", originalHome)

	// Create ~/.claude.json with empty mcpServers
	claudeJSON := filepath.Join(tmpHome, ".claude.json")
	content := `{"mcpServers": {}}`
	if err := os.WriteFile(claudeJSON, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	// Test - should return empty slice (not nil)
	names := GetUserMCPNames()
	if names == nil {
		t.Errorf("expected empty slice, got nil")
	}
	if len(names) != 0 {
		t.Errorf("expected 0 MCPs, got %d", len(names))
	}
}

func TestWriteUserMCP(t *testing.T) {
	// Create temp home dir
	tmpHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", originalHome)

	// Create initial ~/.claude.json with other fields to preserve
	claudeJSON := filepath.Join(tmpHome, ".claude.json")
	content := `{"numStartups": 100, "mcpServers": {}}`
	_ = os.WriteFile(claudeJSON, []byte(content), 0644)

	// Test writing empty list (clears MCPs)
	err := WriteUserMCP([]string{})
	if err != nil {
		t.Errorf("WriteUserMCP failed: %v", err)
	}

	// Verify mcpServers is empty but other fields preserved
	data, _ := os.ReadFile(claudeJSON)
	var result map[string]interface{}
	_ = json.Unmarshal(data, &result)

	if result["numStartups"] != float64(100) {
		t.Errorf("expected numStartups=100, got %v", result["numStartups"])
	}

	mcpServers, ok := result["mcpServers"].(map[string]interface{})
	if !ok || len(mcpServers) != 0 {
		t.Errorf("expected empty mcpServers, got %v", mcpServers)
	}
}

func TestWriteUserMCPCreatesFile(t *testing.T) {
	// Create temp home dir (no .claude.json exists)
	tmpHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", originalHome)

	// Test writing to non-existent file
	err := WriteUserMCP([]string{})
	if err != nil {
		t.Errorf("WriteUserMCP failed: %v", err)
	}

	// Verify file was created
	claudeJSON := filepath.Join(tmpHome, ".claude.json")
	data, err := os.ReadFile(claudeJSON)
	if err != nil {
		t.Fatalf("Failed to read created file: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("Failed to parse created file: %v", err)
	}

	mcpServers, ok := result["mcpServers"].(map[string]interface{})
	if !ok {
		t.Error("expected mcpServers field")
	}
	if len(mcpServers) != 0 {
		t.Errorf("expected empty mcpServers, got %v", mcpServers)
	}
}
