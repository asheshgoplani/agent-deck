package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// GetKiroMCPInfo returns MCP server information for a kiro session.
func GetKiroMCPInfo(projectPath string) *MCPInfo {
	info := &MCPInfo{}

	// Expand ~/
	homeDir, _ := os.UserHomeDir()
	if strings.HasPrefix(projectPath, "~/") && homeDir != "" {
		projectPath = filepath.Join(homeDir, projectPath[2:])
	}

	// Read workspace mcp.json
	workspacePath := filepath.Join(projectPath, ".kiro", "settings", "mcp.json")
	if names := readKiroMCPNames(workspacePath); len(names) > 0 {
		for _, n := range names {
			info.LocalMCPs = append(info.LocalMCPs, LocalMCP{Name: n, SourcePath: filepath.Dir(workspacePath)})
		}
	}

	// Read global mcp.json
	globalPath := filepath.Join(homeDir, ".kiro", "settings", "mcp.json")
	if names := readKiroMCPNames(globalPath); len(names) > 0 {
		info.Global = names
	}

	return info
}

func readKiroMCPNames(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cfg struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if json.Unmarshal(data, &cfg) != nil {
		return nil
	}
	names := make([]string, 0, len(cfg.MCPServers))
	for name := range cfg.MCPServers {
		names = append(names, name)
	}
	return names
}
