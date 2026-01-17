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
