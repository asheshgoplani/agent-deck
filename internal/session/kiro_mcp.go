package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// GetKiroMCPInfo returns MCP server information for a kiro session.
func GetKiroMCPInfo(projectPath string) *MCPInfo {
	info := &MCPInfo{}

	// Read workspace mcp.json
	workspacePath := filepath.Join(projectPath, ".kiro", "settings", "mcp.json")
	if names := readKiroMCPNames(workspacePath); len(names) > 0 {
		for _, n := range names {
			info.LocalMCPs = append(info.LocalMCPs, LocalMCP{Name: n, SourcePath: filepath.Dir(workspacePath)})
		}
	}

	// Read global mcp.json
	homeDir, _ := os.UserHomeDir()
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

// GetKiroMCPNames returns all MCP server names for a kiro project.
func GetKiroMCPNames(projectPath string) []string {
	info := GetKiroMCPInfo(projectPath)
	if info == nil {
		return nil
	}
	all := make([]string, 0, len(info.Global)+len(info.LocalMCPs))
	all = append(all, info.Global...)
	for _, m := range info.LocalMCPs {
		all = append(all, m.Name)
	}
	return all
}

// WriteKiroMCP writes an MCP server config to kiro's mcp.json.
func WriteKiroMCP(projectPath, name, scope string, serverConfig json.RawMessage) error {
	var path string
	switch scope {
	case "workspace", "local":
		path = filepath.Join(projectPath, ".kiro", "settings", "mcp.json")
	case "global":
		homeDir, _ := os.UserHomeDir()
		path = filepath.Join(homeDir, ".kiro", "settings", "mcp.json")
	default:
		return fmt.Errorf("unknown scope: %s", scope)
	}

	// Read existing
	var cfg struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, &cfg)
	}
	if cfg.MCPServers == nil {
		cfg.MCPServers = make(map[string]json.RawMessage)
	}

	cfg.MCPServers[name] = serverConfig

	// Write
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0644)
}
