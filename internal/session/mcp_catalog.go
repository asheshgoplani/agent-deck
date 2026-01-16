package session

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// MCPServerConfig represents an MCP server configuration (Claude's format)
type MCPServerConfig struct {
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"` // For HTTP transport
}

// getExternalSocketPath returns the socket path if an external pool socket exists and is alive
// This allows CLI commands to use sockets created by the TUI without needing pool initialization
func getExternalSocketPath(mcpName string) string {
	socketPath := filepath.Join("/tmp", fmt.Sprintf("agentdeck-mcp-%s.sock", mcpName))

	// Check if socket file exists
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		return ""
	}

	// Check if socket is alive (accepting connections)
	conn, err := net.DialTimeout("unix", socketPath, 500*time.Millisecond)
	if err != nil {
		log.Printf("[MCP-POOL] Socket %s exists but not alive: %v", socketPath, err)
		return ""
	}
	conn.Close()

	return socketPath
}

// WriteMCPJsonFromConfig writes enabled MCPs from config.toml to project's .mcp.json
func WriteMCPJsonFromConfig(projectPath string, enabledNames []string) error {
	mcpFile := filepath.Join(projectPath, ".mcp.json")
	availableMCPs := GetAvailableMCPs()
	pool := GetGlobalPool() // Get pool instance (may be nil)

	// Build the .mcp.json content using MCPServerConfig format (Claude's expected format)
	mcpConfig := struct {
		MCPServers map[string]MCPServerConfig `json:"mcpServers"`
	}{
		MCPServers: make(map[string]MCPServerConfig),
	}

	for _, name := range enabledNames {
		if def, ok := availableMCPs[name]; ok {
			// Check if this is an HTTP/SSE MCP (has URL configured)
			if def.URL != "" {
				transport := def.Transport
				if transport == "" {
					transport = "http" // default to http if URL is set
				}
				mcpConfig.MCPServers[name] = MCPServerConfig{
					Type: transport,
					URL:  def.URL,
				}
				log.Printf("[MCP] ✓ %s: using %s transport at %s", name, transport, def.URL)
				continue
			}

			// Check if pool exists and should pool this MCP (stdio only)
			if pool != nil && pool.ShouldPool(name) {
				// Check if socket is ready NOW - don't block waiting (Issue #36)
				if pool.IsRunning(name) {
					// Use Unix socket (nc connects to socket proxy)
					socketPath := pool.GetSocketPath(name)
					mcpConfig.MCPServers[name] = MCPServerConfig{
						Command: "nc",
						Args:    []string{"-U", socketPath},
					}
					log.Printf("[MCP-POOL] ✓ %s: using socket %s", name, socketPath)
					continue
				}

				// Socket not ready - check fallback policy
				if !pool.FallbackEnabled() {
					log.Printf("[MCP-POOL] ✗ %s: socket not ready, fallback disabled", name)
					return fmt.Errorf("MCP '%s' socket not ready. Options:\n"+
						"  1. Enable fallback: set fallback_to_stdio = true in config.toml\n"+
						"  2. Wait for pool to initialize and try again\n"+
						"  3. Check MCP is running: ls /tmp/agentdeck-mcp-%s.sock", name, name)
				}
				log.Printf("[MCP-POOL] ⚠️ %s: socket not ready - falling back to stdio", name)
			} else if pool != nil && !pool.ShouldPool(name) {
				// MCP is explicitly excluded from pool - use stdio
				log.Printf("[MCP-POOL] %s: excluded from pool, using stdio", name)
			} else if pool == nil {
				// Pool not initialized (CLI mode) - try to discover external sockets from TUI
				config, _ := LoadUserConfig()
				if config != nil && config.MCPPool.Enabled {
					// Try to find existing socket from TUI's pool
					if socketPath := getExternalSocketPath(name); socketPath != "" {
						mcpConfig.MCPServers[name] = MCPServerConfig{
							Command: "nc",
							Args:    []string{"-U", socketPath},
						}
						log.Printf("[MCP-POOL] ✓ %s: discovered external socket %s", name, socketPath)
						continue
					}
					// Socket not found - check fallback policy
					if !config.MCPPool.FallbackStdio {
						log.Printf("[MCP-POOL] ✗ %s: pool enabled but socket not found - fallback disabled", name)
						return fmt.Errorf("MCP '%s' socket not found. Options:\n"+
							"  1. Enable fallback: set fallback_to_stdio = true in config.toml\n"+
							"  2. Start TUI to initialize pool: agent-deck\n"+
							"  3. Check socket exists: ls /tmp/agentdeck-mcp-%s.sock", name, name)
					}
					log.Printf("[MCP-POOL] ⚠️ %s: socket not found, falling back to stdio", name)
				} else {
					log.Printf("[MCP-POOL] %s: pool disabled, using stdio", name)
				}
			}

			// Fallback to stdio mode (pool disabled, excluded, or socket failed with fallback enabled)
			args := def.Args
			if args == nil {
				args = []string{}
			}
			env := def.Env
			if env == nil {
				env = map[string]string{}
			}
			mcpConfig.MCPServers[name] = MCPServerConfig{
				Type:    "stdio",
				Command: def.Command,
				Args:    args,
				Env:     env,
			}
			log.Printf("[MCP-POOL] ⚠️ %s: using stdio (NOT pooled)", name)
		}
	}

	data, err := json.MarshalIndent(mcpConfig, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal .mcp.json: %w", err)
	}

	// Atomic write
	tmpPath := mcpFile + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write .mcp.json: %w", err)
	}

	if err := os.Rename(tmpPath, mcpFile); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to save .mcp.json: %w", err)
	}

	return nil
}

// WriteGlobalMCP adds or removes MCPs from Claude's global config
// This modifies ~/.claude-work/.claude.json → mcpServers
func WriteGlobalMCP(enabledNames []string) error {
	configDir := GetClaudeConfigDir()
	configFile := filepath.Join(configDir, ".claude.json")

	// Read existing config (preserve other fields like projects, settings, etc.)
	var rawConfig map[string]interface{}
	if data, err := os.ReadFile(configFile); err == nil {
		if err := json.Unmarshal(data, &rawConfig); err != nil {
			rawConfig = make(map[string]interface{})
		}
	} else {
		rawConfig = make(map[string]interface{})
	}

	// Build new mcpServers from enabled names using config.toml definitions
	availableMCPs := GetAvailableMCPs()
	pool := GetGlobalPool() // Get pool instance (may be nil)
	mcpServers := make(map[string]MCPServerConfig)

	for _, name := range enabledNames {
		if def, ok := availableMCPs[name]; ok {
			// Check if this is an HTTP/SSE MCP (has URL configured)
			if def.URL != "" {
				transport := def.Transport
				if transport == "" {
					transport = "http" // default to http if URL is set
				}
				mcpServers[name] = MCPServerConfig{
					Type: transport,
					URL:  def.URL,
				}
				log.Printf("[MCP] ✓ Global %s: using %s transport at %s", name, transport, def.URL)
				continue
			}

			// Check if pool exists and should pool this MCP (stdio only)
			if pool != nil && pool.ShouldPool(name) {
				// Check if socket is ready NOW - don't block waiting (Issue #36)
				if pool.IsRunning(name) {
					// Use Unix socket (nc connects to socket proxy)
					socketPath := pool.GetSocketPath(name)
					mcpServers[name] = MCPServerConfig{
						Command: "nc",
						Args:    []string{"-U", socketPath},
					}
					log.Printf("[MCP-POOL] ✓ Global %s: using socket %s", name, socketPath)
					continue
				}

				// Socket not ready - check fallback policy
				if !pool.FallbackEnabled() {
					log.Printf("[MCP-POOL] ✗ Global %s: socket not ready, fallback disabled", name)
					return fmt.Errorf("MCP '%s' socket not ready. Options:\n"+
						"  1. Enable fallback: set fallback_to_stdio = true in config.toml\n"+
						"  2. Wait for pool to initialize and try again\n"+
						"  3. Check MCP is running: ls /tmp/agentdeck-mcp-%s.sock", name, name)
				}
				log.Printf("[MCP-POOL] ⚠️ Global %s: socket not ready - falling back to stdio", name)
			} else if pool != nil && !pool.ShouldPool(name) {
				// MCP is explicitly excluded from pool - use stdio
				log.Printf("[MCP-POOL] Global %s: excluded from pool, using stdio", name)
			} else if pool == nil {
				// Pool not initialized (CLI mode) - try to discover external sockets from TUI
				config, _ := LoadUserConfig()
				if config != nil && config.MCPPool.Enabled {
					// Try to find existing socket from TUI's pool
					if socketPath := getExternalSocketPath(name); socketPath != "" {
						mcpServers[name] = MCPServerConfig{
							Command: "nc",
							Args:    []string{"-U", socketPath},
						}
						log.Printf("[MCP-POOL] ✓ Global %s: discovered external socket %s", name, socketPath)
						continue
					}
					// Socket not found - check fallback policy
					if !config.MCPPool.FallbackStdio {
						log.Printf("[MCP-POOL] ✗ Global %s: pool enabled but socket not found - fallback disabled", name)
						return fmt.Errorf("MCP '%s' socket not found. Options:\n"+
							"  1. Enable fallback: set fallback_to_stdio = true in config.toml\n"+
							"  2. Start TUI to initialize pool: agent-deck\n"+
							"  3. Check socket exists: ls /tmp/agentdeck-mcp-%s.sock", name, name)
					}
					log.Printf("[MCP-POOL] ⚠️ Global %s: socket not found, falling back to stdio", name)
				} else {
					log.Printf("[MCP-POOL] Global %s: pool disabled, using stdio", name)
				}
			}

			// Fallback to stdio mode (pool disabled, excluded, or socket failed with fallback enabled)
			args := def.Args
			if args == nil {
				args = []string{}
			}
			env := def.Env
			if env == nil {
				env = map[string]string{}
			}
			mcpServers[name] = MCPServerConfig{
				Type:    "stdio",
				Command: def.Command,
				Args:    args,
				Env:     env,
			}
			log.Printf("[MCP-POOL] ⚠️ Global %s: using stdio (NOT pooled)", name)
		}
	}

	rawConfig["mcpServers"] = mcpServers

	// Write atomically
	data, err := json.MarshalIndent(rawConfig, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	tmpPath := configFile + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	if err := os.Rename(tmpPath, configFile); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to save config: %w", err)
	}

	return nil
}

// GetGlobalMCPNames returns the names of MCPs currently in Claude's global config
func GetGlobalMCPNames() []string {
	configDir := GetClaudeConfigDir()
	configFile := filepath.Join(configDir, ".claude.json")

	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil
	}

	var config struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil
	}

	names := make([]string, 0, len(config.MCPServers))
	for name := range config.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// GetProjectMCPNames returns MCPs from projects[path].mcpServers in Claude's config
func GetProjectMCPNames(projectPath string) []string {
	configDir := GetClaudeConfigDir()
	configFile := filepath.Join(configDir, ".claude.json")

	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil
	}

	var config struct {
		Projects map[string]struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil
	}

	proj, ok := config.Projects[projectPath]
	if !ok {
		return nil
	}

	names := make([]string, 0, len(proj.MCPServers))
	for name := range proj.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ClearProjectMCPs removes all MCPs from projects[path].mcpServers in Claude's config
func ClearProjectMCPs(projectPath string) error {
	configDir := GetClaudeConfigDir()
	configFile := filepath.Join(configDir, ".claude.json")

	// Read existing config
	data, err := os.ReadFile(configFile)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	var rawConfig map[string]interface{}
	if err := json.Unmarshal(data, &rawConfig); err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	// Get projects map
	projects, ok := rawConfig["projects"].(map[string]interface{})
	if !ok {
		return nil // No projects, nothing to clear
	}

	// Get specific project
	proj, ok := projects[projectPath].(map[string]interface{})
	if !ok {
		return nil // Project not found, nothing to clear
	}

	// Clear mcpServers for this project
	proj["mcpServers"] = map[string]interface{}{}

	// Write atomically
	newData, err := json.MarshalIndent(rawConfig, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	tmpPath := configFile + ".tmp"
	if err := os.WriteFile(tmpPath, newData, 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	if err := os.Rename(tmpPath, configFile); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to save config: %w", err)
	}

	return nil
}
