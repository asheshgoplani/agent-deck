package session

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
)

// GetCursorConfigDir returns ~/.cursor (Cursor IDE / Agent CLI config).
func GetCursorConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".cursor")
}

// cursorProjectMCPPath resolves <project>/.cursor/mcp.json from a session project directory.
// projectPath comes from agent-deck session metadata (local workspace), not remote input.
func cursorProjectMCPPath(projectPath string) (string, error) {
	if projectPath == "" {
		return "", fmt.Errorf("empty project path")
	}
	root, err := filepath.Abs(filepath.Clean(projectPath))
	if err != nil {
		return "", err
	}
	mcpFile := filepath.Join(root, ".cursor", "mcp.json")
	rel, err := filepath.Rel(root, mcpFile)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("cursor mcp path outside project: %s", projectPath)
	}
	return mcpFile, nil
}

func cursorGlobalMCPPath() string {
	dir := GetCursorConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "mcp.json")
}

var (
	cursorMcpInfoCache   = make(map[string]*MCPInfo)
	cursorMcpInfoCacheMu sync.RWMutex
	cursorMcpCacheTimes  = make(map[string]time.Time)
)

// GetCursorMCPInfo reads Cursor Agent CLI MCP config: ~/.cursor/mcp.json (global)
// and <project>/.cursor/mcp.json (project). Matches Cursor docs merge semantics for display.
func GetCursorMCPInfo(projectPath string) *MCPInfo {
	cursorMcpInfoCacheMu.RLock()
	if cached, ok := cursorMcpInfoCache[projectPath]; ok {
		if time.Since(cursorMcpCacheTimes[projectPath]) < mcpCacheExpiry {
			cursorMcpInfoCacheMu.RUnlock()
			return cached
		}
	}
	cursorMcpInfoCacheMu.RUnlock()

	info := getCursorMCPInfoUncached(projectPath)

	cursorMcpInfoCacheMu.Lock()
	cursorMcpInfoCache[projectPath] = info
	cursorMcpCacheTimes[projectPath] = time.Now()
	cursorMcpInfoCacheMu.Unlock()

	return info
}

func getCursorMCPInfoUncached(projectPath string) *MCPInfo {
	info := &MCPInfo{}

	if gpath := cursorGlobalMCPPath(); gpath != "" {
		if data, err := os.ReadFile(gpath); err == nil {
			var cfg projectMCPConfig
			if json.Unmarshal(data, &cfg) == nil && cfg.MCPServers != nil {
				for name := range cfg.MCPServers {
					info.Global = append(info.Global, name)
				}
			}
		}
	}

	if projectPath != "" {
		p, err := cursorProjectMCPPath(projectPath)
		if err == nil {
			if data, readErr := os.ReadFile(p); readErr == nil {
				var cfg projectMCPConfig
				if json.Unmarshal(data, &cfg) == nil && cfg.MCPServers != nil {
					for name := range cfg.MCPServers {
						info.LocalMCPs = append(info.LocalMCPs, LocalMCP{
							Name:       name,
							SourcePath: projectPath,
						})
					}
				}
			}
		}
	}

	sort.Strings(info.Global)
	sort.Slice(info.LocalMCPs, func(i, j int) bool {
		return info.LocalMCPs[i].Name < info.LocalMCPs[j].Name
	})
	return info
}

// ClearCursorMCPCache invalidates cached Cursor MCP info for a project path.
func ClearCursorMCPCache(projectPath string) {
	cursorMcpInfoCacheMu.Lock()
	defer cursorMcpInfoCacheMu.Unlock()
	delete(cursorMcpInfoCache, projectPath)
	delete(cursorMcpCacheTimes, projectPath)
}

// ClearAllCursorMCPInfoCache clears all Cursor MCP cache entries (needed after global ~/.cursor/mcp.json writes).
func ClearAllCursorMCPInfoCache() {
	cursorMcpInfoCacheMu.Lock()
	defer cursorMcpInfoCacheMu.Unlock()
	clear(cursorMcpInfoCache)
	clear(cursorMcpCacheTimes)
}

// PruneCursorMCPCache removes stale Cursor MCP cache entries (TTL), like PruneMCPCache.
func PruneCursorMCPCache(maxAge time.Duration) {
	cursorMcpInfoCacheMu.Lock()
	defer cursorMcpInfoCacheMu.Unlock()
	now := time.Now()
	for path, t := range cursorMcpCacheTimes {
		if now.Sub(t) > maxAge {
			delete(cursorMcpInfoCache, path)
			delete(cursorMcpCacheTimes, path)
		}
	}
}

// WriteCursorProjectMCP writes catalog MCPs to <project>/.cursor/mcp.json (Cursor Agent CLI).
func WriteCursorProjectMCP(projectPath string, enabledNames []string) error {
	if !GetManageMCPJson() {
		mcpCatLog.Debug("cursor_mcp_json_management_disabled", "path", projectPath)
		return nil
	}
	if projectPath == "" {
		return fmt.Errorf("cursor project MCP: empty project path")
	}
	mcpFile, err := cursorProjectMCPPath(projectPath)
	if err != nil {
		return err
	}
	dir := filepath.Dir(mcpFile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create .cursor directory: %w", err)
	}
	if err := WriteMergedMcpJSONFile(mcpFile, enabledNames, ""); err != nil {
		return err
	}
	return nil
}

// WriteCursorGlobalMCP writes catalog MCPs to ~/.cursor/mcp.json. Preserves other JSON keys if present.
func WriteCursorGlobalMCP(enabledNames []string) error {
	if !GetManageMCPJson() {
		mcpCatLog.Debug("cursor_mcp_json_management_disabled", "scope", "global")
		return nil
	}
	configFile := cursorGlobalMCPPath()
	if configFile == "" {
		return fmt.Errorf("cannot resolve ~/.cursor")
	}
	if err := os.MkdirAll(filepath.Dir(configFile), 0o755); err != nil {
		return fmt.Errorf("create cursor config dir: %w", err)
	}

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
			if def.URL != "" {
				if def.HasAutoStartServer() {
					if err := StartHTTPServer(name, &def); err != nil {
						mcpCatLog.Warn("http_server_start_failed", slog.String("mcp", name), slog.String("scope", "cursor-global"), slog.Any("error", err))
					}
				}

				transport := def.Transport
				if transport == "" {
					transport = "http"
				}
				mcpServers[name] = MCPServerConfig{
					Type:    transport,
					URL:     def.URL,
					Headers: def.Headers,
				}
				continue
			}

			if socketCfg, used := tryPoolSocket(pool, name, "global"); used {
				mcpServers[name] = socketCfg
				continue
			}

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
		}
	}

	rawConfig["mcpServers"] = mcpServers

	newData, err := json.MarshalIndent(rawConfig, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cursor mcp.json: %w", err)
	}

	if err := atomicfile.WriteFile(configFile, newData, 0o600); err != nil {
		return fmt.Errorf("save cursor mcp.json: %w", err)
	}

	ClearAllCursorMCPInfoCache()
	return nil
}
