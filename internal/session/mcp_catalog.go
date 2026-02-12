package session

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/mcppool"
)

var mcpCatLog = logging.ForComponent(logging.CompMCP)

// MCPServerConfig represents an MCP server configuration (Claude's format)
type MCPServerConfig struct {
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`     // For HTTP transport
	Headers map[string]string `json:"headers,omitempty"` // For HTTP transport (e.g., Authorization)
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
		mcpCatLog.Debug("socket_not_alive", slog.String("socket", socketPath), slog.Any("error", err))
		return ""
	}
	conn.Close()

	return socketPath
}

// tryPoolSocket attempts to resolve an MCP to a pool socket in order of preference:
//  1. pool.IsRunning (in-memory check, fastest)
//  2. Disk socket check (handles pool init race / stale in-memory state)
//  3. Fallback to stdio (last resort, logged as error for visibility)
//
// Returns (config, true) if socket was found, or (empty, false) to fall through to stdio.
func tryPoolSocket(pool *mcppool.Pool, name, scope string) (MCPServerConfig, bool) {
	// Case 1: Pool exists and should manage this MCP
	if pool != nil && pool.ShouldPool(name) {
		// Try in-memory pool state first (fastest)
		if pool.IsRunning(name) {
			socketPath := pool.GetSocketPath(name)
			mcpCatLog.Info("transport_socket", slog.String("mcp", name), slog.String("scope", scope), slog.String("socket", socketPath))
			return MCPServerConfig{
				Command: "agent-deck",
				Args:    []string{"mcp-proxy", socketPath},
			}, true
		}

		// Pool says not running, but check if socket exists on disk
		// (handles race during pool initialization or stale in-memory state)
		if socketPath := getExternalSocketPath(name); socketPath != "" {
			mcpCatLog.Warn("pool_stale_disk_recovery", slog.String("mcp", name), slog.String("scope", scope),
				slog.String("socket", socketPath),
				slog.String("detail", "pool.IsRunning=false but socket alive on disk, using disk socket"))
			return MCPServerConfig{
				Command: "agent-deck",
				Args:    []string{"mcp-proxy", socketPath},
			}, true
		}

		// Socket truly not available
		if !pool.FallbackEnabled() {
			mcpCatLog.Error("socket_not_ready_no_fallback", slog.String("mcp", name), slog.String("scope", scope))
			// Return false to let caller handle the error
			return MCPServerConfig{}, false
		}
		mcpCatLog.Error("STDIO_FALLBACK", slog.String("mcp", name), slog.String("scope", scope),
			slog.String("reason", "pool_socket_not_ready"),
			slog.String("impact", "spawning full MCP process, wastes RAM"),
			slog.String("fix", "restart session after pool is ready"))
		return MCPServerConfig{}, false
	}

	// Case 2: Pool exists but this MCP is excluded
	if pool != nil && !pool.ShouldPool(name) {
		mcpCatLog.Debug("pool_excluded", slog.String("mcp", name), slog.String("scope", scope))
		return MCPServerConfig{}, false
	}

	// Case 3: No pool (CLI mode) - try to discover external sockets from TUI
	if pool == nil {
		config, _ := LoadUserConfig()
		if config != nil && config.MCPPool.Enabled {
			if socketPath := getExternalSocketPath(name); socketPath != "" {
				mcpCatLog.Info("external_socket_discovered", slog.String("mcp", name), slog.String("scope", scope), slog.String("socket", socketPath))
				return MCPServerConfig{
					Command: "agent-deck",
					Args:    []string{"mcp-proxy", socketPath},
				}, true
			}
			if !config.MCPPool.FallbackStdio {
				mcpCatLog.Error("socket_not_found_no_fallback", slog.String("mcp", name), slog.String("scope", scope))
				return MCPServerConfig{}, false
			}
			mcpCatLog.Error("STDIO_FALLBACK", slog.String("mcp", name), slog.String("scope", scope),
				slog.String("reason", "cli_mode_socket_not_found"),
				slog.String("impact", "spawning full MCP process, wastes RAM"),
				slog.String("fix", "ensure TUI is running with pool before creating sessions"))
			return MCPServerConfig{}, false
		}
		mcpCatLog.Debug("pool_disabled", slog.String("mcp", name), slog.String("scope", scope))
	}

	return MCPServerConfig{}, false
}

// readExistingLocalMCPServers reads mcpServers from an existing .mcp.json file.
// Returns nil if the file doesn't exist or can't be parsed.
func readExistingLocalMCPServers(mcpFile string) map[string]json.RawMessage {
	data, err := os.ReadFile(mcpFile)
	if err != nil {
		return nil
	}
	var config struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil
	}
	return config.MCPServers
}

// WriteMCPJsonFromConfig writes enabled MCPs from config.toml to project's .mcp.json
// It preserves any existing entries not managed by agent-deck (not defined in config.toml)
func WriteMCPJsonFromConfig(projectPath string, enabledNames []string) error {
	mcpFile := filepath.Join(projectPath, ".mcp.json")
	availableMCPs := GetAvailableMCPs()
	pool := GetGlobalPool() // Get pool instance (may be nil)

	// Read existing .mcp.json to preserve entries not managed by agent-deck (#146)
	existingServers := readExistingLocalMCPServers(mcpFile)

	// Build agent-deck managed MCP entries
	agentDeckServers := make(map[string]MCPServerConfig)

	for _, name := range enabledNames {
		if def, ok := availableMCPs[name]; ok {
			// Check if this is an HTTP/SSE MCP (has URL configured)
			if def.URL != "" {
				// Start HTTP server if configured
				if def.HasAutoStartServer() {
					if err := StartHTTPServer(name, &def); err != nil {
						mcpCatLog.Warn("http_server_start_failed", slog.String("mcp", name), slog.String("scope", "local"), slog.Any("error", err))
						// Continue anyway - server might be external or user will troubleshoot
					}
				}

				transport := def.Transport
				if transport == "" {
					transport = "http" // default to http if URL is set
				}
				agentDeckServers[name] = MCPServerConfig{
					Type:    transport,
					URL:     def.URL,
					Headers: def.Headers,
				}
				mcpCatLog.Info("transport_http", slog.String("mcp", name), slog.String("scope", "local"), slog.String("transport", transport), slog.String("url", def.URL))
				continue
			}

			// Try to use pool socket for this MCP (stdio only)
			if socketCfg, used := tryPoolSocket(pool, name, "local"); used {
				agentDeckServers[name] = socketCfg
				continue
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
			agentDeckServers[name] = MCPServerConfig{
				Type:    "stdio",
				Command: def.Command,
				Args:    args,
				Env:     env,
			}
			mcpCatLog.Info("transport_stdio", slog.String("mcp", name), slog.String("scope", "local"))
		}
	}

	// Merge: preserve non-agent-deck entries, then add agent-deck entries (#146)
	mergedServers := make(map[string]json.RawMessage)
	for name, raw := range existingServers {
		if _, managed := availableMCPs[name]; !managed {
			mergedServers[name] = raw
			mcpCatLog.Debug("preserved_existing_mcp", slog.String("mcp", name), slog.String("scope", "local"))
		}
	}
	for name, cfg := range agentDeckServers {
		raw, err := json.Marshal(cfg)
		if err != nil {
			mcpCatLog.Warn("marshal_mcp_entry_failed", slog.String("mcp", name), slog.Any("error", err))
			continue
		}
		mergedServers[name] = raw
	}

	finalConfig := struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}{
		MCPServers: mergedServers,
	}

	data, err := json.MarshalIndent(finalConfig, "", "  ")
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
				// Start HTTP server if configured
				if def.HasAutoStartServer() {
					if err := StartHTTPServer(name, &def); err != nil {
						mcpCatLog.Warn("http_server_start_failed", slog.String("mcp", name), slog.String("scope", "global"), slog.Any("error", err))
					}
				}

				transport := def.Transport
				if transport == "" {
					transport = "http" // default to http if URL is set
				}
				mcpServers[name] = MCPServerConfig{
					Type:    transport,
					URL:     def.URL,
					Headers: def.Headers,
				}
				mcpCatLog.Info("transport_http", slog.String("mcp", name), slog.String("scope", "global"), slog.String("transport", transport), slog.String("url", def.URL))
				continue
			}

			// Try to use pool socket for this MCP (stdio only)
			if socketCfg, used := tryPoolSocket(pool, name, "global"); used {
				mcpServers[name] = socketCfg
				continue
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
			mcpCatLog.Info("transport_stdio", slog.String("mcp", name), slog.String("scope", "global"))
		}
	}

	// Merge: preserve non-agent-deck entries from existing config (#146)
	mergedMCPs := make(map[string]interface{})
	if existingMCPs, ok := rawConfig["mcpServers"].(map[string]interface{}); ok {
		for name, cfg := range existingMCPs {
			if _, managed := availableMCPs[name]; !managed {
				mergedMCPs[name] = cfg
			}
		}
	}
	for name, cfg := range mcpServers {
		mergedMCPs[name] = cfg
	}
	rawConfig["mcpServers"] = mergedMCPs

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

// GetUserMCPRootPath returns the path to ~/.claude.json (ROOT config, always read by Claude)
// This is the ROOT config that Claude ALWAYS reads, regardless of CLAUDE_CONFIG_DIR setting.
// MCPs defined here apply to ALL Claude sessions globally.
func GetUserMCPRootPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude.json")
}

// WriteUserMCP writes MCPs to ~/.claude.json (ROOT config)
// Uses socket proxies if pool is running, otherwise falls back to stdio
// WARNING: MCPs written here affect ALL Claude sessions regardless of profile!
func WriteUserMCP(enabledNames []string) error {
	configFile := GetUserMCPRootPath()
	if configFile == "" {
		return fmt.Errorf("could not determine home directory")
	}

	// Read existing config (preserve other fields like numStartups, projects, etc.)
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
				// Start HTTP server if configured
				if def.HasAutoStartServer() {
					if err := StartHTTPServer(name, &def); err != nil {
						mcpCatLog.Warn("http_server_start_failed", slog.String("mcp", name), slog.String("scope", "user"), slog.Any("error", err))
					}
				}

				transport := def.Transport
				if transport == "" {
					transport = "http" // default to http if URL is set
				}
				mcpServers[name] = MCPServerConfig{
					Type:    transport,
					URL:     def.URL,
					Headers: def.Headers,
				}
				mcpCatLog.Info("transport_http", slog.String("mcp", name), slog.String("scope", "user"), slog.String("transport", transport), slog.String("url", def.URL))
				continue
			}

			// Try to use pool socket for this MCP (stdio only)
			if socketCfg, used := tryPoolSocket(pool, name, "user"); used {
				mcpServers[name] = socketCfg
				continue
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
			mcpCatLog.Info("transport_stdio", slog.String("mcp", name), slog.String("scope", "user"))
		}
	}

	// Merge: preserve non-agent-deck entries from existing config (#146)
	mergedMCPs := make(map[string]interface{})
	if existingMCPs, ok := rawConfig["mcpServers"].(map[string]interface{}); ok {
		for name, cfg := range existingMCPs {
			if _, managed := availableMCPs[name]; !managed {
				mergedMCPs[name] = cfg
			}
		}
	}
	for name, cfg := range mcpServers {
		mergedMCPs[name] = cfg
	}
	rawConfig["mcpServers"] = mergedMCPs

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

// GetUserMCPNames returns the names of MCPs in ~/.claude.json (ROOT config)
// These MCPs are loaded by ALL Claude sessions regardless of CLAUDE_CONFIG_DIR.
// This is different from GetGlobalMCPNames which reads from $CLAUDE_CONFIG_DIR/.claude.json
func GetUserMCPNames() []string {
	configFile := GetUserMCPRootPath()
	if configFile == "" {
		return nil
	}

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
