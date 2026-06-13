package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
)

// opencodeMCPServer represents one server entry in opencode.json under "mcp".
type opencodeMCPServer struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	URL         string            `json:"url,omitempty"`
}

// opencodeConfig is the top-level shape of opencode.json (local or global).
// Only the "mcp" key is relevant here; all other keys are preserved via rawConfig.
type opencodeConfig struct {
	MCP map[string]opencodeMCPServer `json:"mcp,omitempty"`
}

// opencodeMCPConfigDirOverride allows tests to override ~/.config/opencode.
var opencodeMCPConfigDirOverride string

// GetOpenCodeConfigDir returns ~/.config/opencode (OpenCode global config directory).
func GetOpenCodeConfigDir() string {
	if opencodeMCPConfigDirOverride != "" {
		return opencodeMCPConfigDirOverride
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "opencode")
}

// opencodeProjectMCPPath resolves <project>/opencode.json.
// projectPath comes from agent-deck session metadata (local workspace), not remote input.
func opencodeProjectMCPPath(projectPath string) (string, error) {
	if projectPath == "" {
		return "", fmt.Errorf("empty project path")
	}
	root, err := filepath.Abs(filepath.Clean(projectPath))
	if err != nil {
		return "", err
	}
	mcpFile := filepath.Join(root, "opencode.json")
	rel, err := filepath.Rel(root, mcpFile)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("opencode mcp path outside project: %s", projectPath)
	}
	return mcpFile, nil
}

func opencodeGlobalMCPPath() string {
	dir := GetOpenCodeConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "opencode.json")
}

var (
	opencodeMcpInfoCache   = make(map[string]*MCPInfo)
	opencodeMcpInfoCacheMu sync.RWMutex
	opencodeMcpCacheTimes  = make(map[string]time.Time)
)

// GetOpenCodeMCPInfo reads OpenCode MCP config: ~/.config/opencode/opencode.json (global)
// and <project>/opencode.json (project-local). Returns merged MCPInfo for display.
func GetOpenCodeMCPInfo(projectPath string) *MCPInfo {
	opencodeMcpInfoCacheMu.RLock()
	if cached, ok := opencodeMcpInfoCache[projectPath]; ok {
		if time.Since(opencodeMcpCacheTimes[projectPath]) < mcpCacheExpiry {
			opencodeMcpInfoCacheMu.RUnlock()
			return cached
		}
	}
	opencodeMcpInfoCacheMu.RUnlock()

	info := getOpenCodeMCPInfoUncached(projectPath)

	opencodeMcpInfoCacheMu.Lock()
	opencodeMcpInfoCache[projectPath] = info
	opencodeMcpCacheTimes[projectPath] = time.Now()
	opencodeMcpInfoCacheMu.Unlock()

	return info
}

func getOpenCodeMCPInfoUncached(projectPath string) *MCPInfo {
	info := &MCPInfo{}

	if gpath := opencodeGlobalMCPPath(); gpath != "" {
		if data, err := os.ReadFile(gpath); err == nil {
			var cfg opencodeConfig
			if json.Unmarshal(data, &cfg) == nil {
				for name := range cfg.MCP {
					info.Global = append(info.Global, name)
				}
			}
		}
	}

	if projectPath != "" {
		p, err := opencodeProjectMCPPath(projectPath)
		if err == nil {
			if data, readErr := os.ReadFile(p); readErr == nil {
				var cfg opencodeConfig
				if json.Unmarshal(data, &cfg) == nil {
					for name := range cfg.MCP {
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

// ClearOpenCodeMCPCache invalidates cached OpenCode MCP info for a project path.
func ClearOpenCodeMCPCache(projectPath string) {
	opencodeMcpInfoCacheMu.Lock()
	defer opencodeMcpInfoCacheMu.Unlock()
	delete(opencodeMcpInfoCache, projectPath)
	delete(opencodeMcpCacheTimes, projectPath)
}

// ClearAllOpenCodeMCPInfoCache clears all OpenCode MCP cache entries (needed after global writes).
func ClearAllOpenCodeMCPInfoCache() {
	opencodeMcpInfoCacheMu.Lock()
	defer opencodeMcpInfoCacheMu.Unlock()
	clear(opencodeMcpInfoCache)
	clear(opencodeMcpCacheTimes)
}

// PruneOpenCodeMCPCache removes stale OpenCode MCP cache entries (TTL).
func PruneOpenCodeMCPCache(maxAge time.Duration) {
	opencodeMcpInfoCacheMu.Lock()
	defer opencodeMcpInfoCacheMu.Unlock()
	now := time.Now()
	for path, t := range opencodeMcpCacheTimes {
		if now.Sub(t) > maxAge {
			delete(opencodeMcpInfoCache, path)
			delete(opencodeMcpCacheTimes, path)
		}
	}
}

// buildOpenCodeMCPServers constructs the mcp map for an opencode.json write.
func buildOpenCodeMCPServers(enabledNames []string) map[string]opencodeMCPServer {
	availableMCPs := GetAvailableMCPs()
	pool := GetGlobalPool()

	servers := make(map[string]opencodeMCPServer)
	for _, name := range enabledNames {
		def, ok := availableMCPs[name]
		if !ok {
			continue
		}

		// HTTP / remote server
		if def.URL != "" {
			if def.HasAutoStartServer() {
				if err := StartHTTPServer(name, &def); err != nil {
					mcpCatLog.Warn("http_server_start_failed_opencode", "mcp", name, "error", err)
				}
			}
			servers[name] = opencodeMCPServer{
				Type: "remote",
				URL:  def.URL,
			}
			continue
		}

		// Pool socket -> stdio via nc
		if socketCfg, used := tryPoolSocket(pool, name, "opencode"); used {
			cmd := []string{}
			if socketCfg.Command != "" {
				cmd = append([]string{socketCfg.Command}, socketCfg.Args...)
			}
			servers[name] = opencodeMCPServer{
				Type:    "local",
				Command: cmd,
			}
			continue
		}

		// stdio
		args := def.Args
		if args == nil {
			args = []string{}
		}
		env := def.Env
		if env == nil {
			env = map[string]string{}
		}
		cmd := append([]string{def.Command}, args...)
		servers[name] = opencodeMCPServer{
			Type:        "local",
			Command:     cmd,
			Environment: env,
		}
	}
	return servers
}

// WriteOpenCodeProjectMCP writes catalog MCPs to <project>/opencode.json.
// OpenCode's local config uses {"mcp": {"name": {"type":"local","command":[...]}}} format.
func WriteOpenCodeProjectMCP(projectPath string, enabledNames []string) error {
	if !GetManageMCPJson() {
		mcpCatLog.Debug("opencode_mcp_json_management_disabled", "path", projectPath)
		return nil
	}
	if projectPath == "" {
		return fmt.Errorf("opencode project MCP: empty project path")
	}
	mcpFile, err := opencodeProjectMCPPath(projectPath)
	if err != nil {
		return err
	}

	// Read existing file to preserve non-mcp keys.
	var rawConfig map[string]interface{}
	if data, readErr := os.ReadFile(mcpFile); readErr == nil {
		if jsonErr := json.Unmarshal(data, &rawConfig); jsonErr != nil {
			rawConfig = make(map[string]interface{})
		}
	} else {
		rawConfig = make(map[string]interface{})
	}

	rawConfig["mcp"] = buildOpenCodeMCPServers(enabledNames)

	newData, err := json.MarshalIndent(rawConfig, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal opencode project mcp: %w", err)
	}
	if err := atomicfile.WriteFile(mcpFile, newData, 0o600); err != nil {
		return fmt.Errorf("save opencode project mcp: %w", err)
	}

	ClearOpenCodeMCPCache(projectPath)
	return nil
}

// WriteOpenCodeGlobalMCP writes catalog MCPs to ~/.config/opencode/opencode.json.
// Preserves other JSON keys already present in the file.
func WriteOpenCodeGlobalMCP(enabledNames []string) error {
	if !GetManageMCPJson() {
		mcpCatLog.Debug("opencode_mcp_json_management_disabled", "scope", "global")
		return nil
	}
	configFile := opencodeGlobalMCPPath()
	if configFile == "" {
		return fmt.Errorf("cannot resolve ~/.config/opencode")
	}
	if err := os.MkdirAll(filepath.Dir(configFile), 0o755); err != nil {
		return fmt.Errorf("create opencode config dir: %w", err)
	}

	var rawConfig map[string]interface{}
	if data, err := os.ReadFile(configFile); err == nil {
		if err := json.Unmarshal(data, &rawConfig); err != nil {
			rawConfig = make(map[string]interface{})
		}
	} else {
		rawConfig = make(map[string]interface{})
	}

	rawConfig["mcp"] = buildOpenCodeMCPServers(enabledNames)

	newData, err := json.MarshalIndent(rawConfig, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal opencode global mcp: %w", err)
	}
	if err := atomicfile.WriteFile(configFile, newData, 0o600); err != nil {
		return fmt.Errorf("save opencode global mcp: %w", err)
	}

	ClearAllOpenCodeMCPInfoCache()
	return nil
}
