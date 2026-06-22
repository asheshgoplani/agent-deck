package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
)

// AntigravityMCPConfig represents ~/.gemini/config/mcp_config.json structure
type AntigravityMCPConfig struct {
	MCPServers map[string]MCPServerConfig `json:"mcpServers"`
}

// GetAntigravityMCPInfo reads MCP configuration from mcp_config.json
func GetAntigravityMCPInfo(_ string) *MCPInfo {
	configFile := filepath.Join(GetAntigravityConfigDir(), "mcp_config.json")

	data, err := os.ReadFile(configFile)
	if err != nil {
		return &MCPInfo{}
	}

	var config AntigravityMCPConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return &MCPInfo{}
	}

	info := &MCPInfo{}
	for name := range config.MCPServers {
		info.Global = append(info.Global, name)
	}
	sort.Strings(info.Global)
	return info
}

// WriteAntigravityMCPSettings writes MCPs to ~/.gemini/config/mcp_config.json
func WriteAntigravityMCPSettings(enabledNames []string) error {
	configFile := filepath.Join(GetAntigravityConfigDir(), "mcp_config.json")

	var rawConfig map[string]interface{}
	if data, err := os.ReadFile(configFile); err == nil {
		if err := json.Unmarshal(data, &rawConfig); err != nil {
			rawConfig = make(map[string]interface{})
		}
	} else {
		rawConfig = make(map[string]interface{})
	}

	availableMCPs := GetAvailableMCPs()
	pool := GetGlobalPool()

	mcpServers := make(map[string]MCPServerConfig)
	for _, name := range enabledNames {
		if def, ok := availableMCPs[name]; ok {
			if pool != nil && pool.ShouldPool(name) && pool.IsRunning(name) {
				socketPath := pool.GetSocketPath(name)
				mcpServers[name] = MCPServerConfig{
					Command: "nc",
					Args:    []string{"-U", socketPath},
				}
			} else {
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

	newData, err := json.MarshalIndent(rawConfig, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(configFile), 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := atomicfile.WriteFile(configFile, newData, 0600); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	return nil
}

// GetAntigravityMCPNames returns configured MCP server names
func GetAntigravityMCPNames() []string {
	info := GetAntigravityMCPInfo("")
	return info.Global
}
