package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
)

// GeminiMCPConfig represents settings.json structure
// VERIFIED: Actual settings.json does NOT have mcp.allowed/excluded
// (Simplified structure compared to research docs)
type GeminiMCPConfig struct {
	MCPServers map[string]MCPServerConfig `json:"mcpServers"`
	// Note: No MCP global settings in actual Gemini settings.json
}

// GetGeminiMCPInfo reads MCP configuration from settings.json
// Returns MCPInfo with Global MCPs only (Gemini has no project-level MCPs)
// VERIFIED: settings.json structure is simple {"mcpServers": {...}}
func GetGeminiMCPInfo(projectPath string) *MCPInfo {
	configFile := filepath.Join(GetGeminiConfigDir(), "settings.json")

	data, err := os.ReadFile(configFile)
	if err != nil {
		return &MCPInfo{}
	}

	var config GeminiMCPConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return &MCPInfo{}
	}

	info := &MCPInfo{}

	// All MCPs are global in Gemini (no project or local MCPs)
	for name := range config.MCPServers {
		info.Global = append(info.Global, name)
	}

	sort.Strings(info.Global)
	return info
}

// WriteGeminiMCPSettings writes MCPs to ~/.gemini/settings.json
// Preserves existing config fields (security, theme, etc.)
// Uses a symlink-preserving atomic write (see internal/atomicfile)
func WriteGeminiMCPSettings(enabledNames []string) error {
	configFile := filepath.Join(GetGeminiConfigDir(), "settings.json")

	// Read existing config (preserve other fields like security)
	var rawConfig map[string]interface{}
	if data, err := os.ReadFile(configFile); err == nil {
		if err := json.Unmarshal(data, &rawConfig); err != nil {
			rawConfig = make(map[string]interface{})
		}
	} else {
		rawConfig = make(map[string]interface{})
	}

	// Get available MCPs from agent-deck config.toml
	availableMCPs := GetAvailableMCPs()
	pool := GetGlobalPool()

	mcpServers := make(map[string]MCPServerConfig)
	for _, name := range enabledNames {
		if def, ok := availableMCPs[name]; ok {
			// Check if should use socket pool mode
			if pool != nil && pool.ShouldPool(name) && pool.IsRunning(name) {
				// Use Unix socket
				socketPath := pool.GetSocketPath(name)
				mcpServers[name] = MCPServerConfig{
					Command: "nc",
					Args:    []string{"-U", socketPath},
				}
			} else {
				// Use stdio mode
				args := def.Args
				if args == nil {
					args = []string{}
				}
				env := def.Env
				if env == nil {
					env = map[string]string{}
				}
				mcpServers[name] = MCPServerConfig{
					Command: def.Command,
					Args:    args,
					Env:     env,
				}
			}
		}
	}

	rawConfig["mcpServers"] = mcpServers

	// Write atomically
	newData, err := json.MarshalIndent(rawConfig, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := atomicfile.WriteFile(configFile, newData, 0600); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	return nil
}

// GetGeminiMCPNames returns names of configured MCPs from settings.json
func GetGeminiMCPNames() []string {
	info := GetGeminiMCPInfo("")
	return info.Global
}

// contains checks if a slice contains a string
// Helper function for tests and internal use
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
