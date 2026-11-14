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
	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// opencodeMCPServer represents one server entry in opencode.json under "mcp".
type opencodeMCPServer struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	URL         string            `json:"url,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
}

// opencodeConfig is the top-level shape of opencode.json (local or global).
// Only the "mcp" key is relevant here; all other keys are preserved via rawConfig.
type opencodeConfig struct {
	MCP map[string]opencodeMCPServer `json:"mcp,omitempty"`
}

// UnmarshalJSON reads both legacy mcp maps and native V2 mcp.servers maps.
func (c *opencodeConfig) UnmarshalJSON(data []byte) error {
	var raw struct {
		MCP map[string]json.RawMessage `json:"mcp"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if servers, ok := raw.MCP["servers"]; ok {
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(servers, &entry); err != nil {
			return err
		}
		// A legacy server named "servers" has a string "type" field; in the V2
		// container "type" can only be a server name whose value is an object.
		var legacyType interface{}
		_ = json.Unmarshal(entry["type"], &legacyType)
		if _, legacyServer := legacyType.(string); !legacyServer {
			return json.Unmarshal(servers, &c.MCP)
		}
	}
	payload, err := json.Marshal(raw.MCP)
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, &c.MCP)
}

// setOpenCodeMCPServers merges the managed servers into the mcp map. Only
// entries whose name is in the agent-deck catalog are replaced or removed;
// every other entry (hand-configured servers) is left as is. For a managed
// entry that already exists, keys agent-deck does not set (headers when none
// are configured, timeout, enabled, ...) are kept. Native V2 options such as
// timeouts are preserved too.
func setOpenCodeMCPServers(config map[string]interface{}, servers map[string]opencodeMCPServer, v2 bool) {
	mcp, _ := config["mcp"].(map[string]interface{})
	existingServers, _ := mcp["servers"].(map[string]interface{})
	_, native := mcp["servers"]
	if _, legacyServer := existingServers["type"].(string); legacyServer {
		// A legacy server named "servers", not the V2 container: its "type"
		// is a string field, while a V2 server named "type" is an object (the
		// reader in UnmarshalJSON makes the same distinction).
		native = false
		existingServers = nil
	}
	if !v2 && !native {
		config["mcp"] = mergeOpenCodeServerMap(mcp, servers)
		return
	}
	if mcp == nil {
		mcp = make(map[string]interface{})
	}
	if !native {
		// Moving to native V2: carry legacy direct entries into servers
		// rather than dropping hand-configured ones.
		existingServers = make(map[string]interface{})
		for name, value := range mcp {
			if server, ok := value.(map[string]interface{}); ok && server["type"] != nil {
				existingServers[name] = value
				delete(mcp, name)
			}
		}
	}
	mcp["servers"] = mergeOpenCodeServerMap(existingServers, servers)
	config["mcp"] = mcp
}

// mergeOpenCodeServerMap returns existing with the managed (catalog) names
// replaced by the desired servers. Every existing entry whose name is in the
// catalog is dropped unless it is in desired, so desired must be built from
// the full set of agent-deck-managed names (a catalog-named entry left out of
// it is removed). Entries not in the catalog are never touched. When a managed
// entry keeps its type and target (same url for remote, same command for
// local), keys agent-deck does not emit are kept; when its type or target
// changes, only timeout and enabled carry over so stored headers, oauth or
// environment are never sent to a different endpoint or command.
func mergeOpenCodeServerMap(existing map[string]interface{}, desired map[string]opencodeMCPServer) map[string]interface{} {
	managed := GetAvailableMCPs()
	out := make(map[string]interface{}, len(existing)+len(desired))
	for name, value := range existing {
		if _, isManaged := managed[name]; !isManaged {
			out[name] = value
		}
	}
	for name, srv := range desired {
		entry := map[string]interface{}{}
		if prev, ok := existing[name].(map[string]interface{}); ok {
			prevType, _ := prev["type"].(string)
			sameType := prevType == "" || prevType == srv.Type
			keepAll := sameType && sameOpenCodeTarget(prev, srv)
			for k, v := range prev {
				if keepAll || k == "timeout" || k == "enabled" {
					entry[k] = v
				}
			}
		}
		if data, err := json.Marshal(srv); err == nil {
			var fields map[string]interface{}
			if json.Unmarshal(data, &fields) == nil {
				for k, v := range fields {
					entry[k] = v
				}
			}
		}
		out[name] = entry
	}
	return out
}

// sameOpenCodeTarget reports whether prev points at the same endpoint (remote)
// or command (local) as srv, so credentials stored on prev were meant for it.
func sameOpenCodeTarget(prev map[string]interface{}, srv opencodeMCPServer) bool {
	if srv.Type == "remote" {
		url, _ := prev["url"].(string)
		return url == srv.URL
	}
	prevCmd, err := json.Marshal(prev["command"])
	if err != nil {
		return false
	}
	cmd, err := json.Marshal(srv.Command)
	return err == nil && string(prevCmd) == string(cmd)
}

// opencodeMCPConfigDirOverride allows tests to override ~/.config/opencode.
var opencodeMCPConfigDirOverride string

// GetOpenCodeConfigDir returns ~/.config/opencode (OpenCode global config directory).
func GetOpenCodeConfigDir() string {
	if opencodeMCPConfigDirOverride != "" {
		return opencodeMCPConfigDirOverride
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return sandboxedToolDir("OpenCode config dir", filepath.Join(dir, "opencode"))
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return sandboxedToolDir("OpenCode config dir", filepath.Join(home, ".config", "opencode"))
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
				Type:    "remote",
				URL:     def.URL,
				Headers: def.Headers,
			}
			continue
		}

		// Pool socket -> stdio via nc
		if socketCfg, used := tryPoolSocket(pool, name, def, "opencode"); used {
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
func WriteOpenCodeProjectMCP(projectPath string, enabledNames []string, nativeV2 ...bool) error {
	if !GetManageMCPJson() {
		mcpCatLog.Debug("opencode_mcp_json_management_disabled", "path", logging.SanitizeValue(projectPath))
		return nil
	}
	if projectPath == "" {
		return fmt.Errorf("opencode project MCP: empty project path")
	}
	mcpFile, err := opencodeProjectMCPPath(projectPath)
	if err != nil {
		return err
	}

	// Read existing file to preserve non-mcp keys. An existing-but-unparseable
	// file must fail closed: resetting rawConfig to an empty map on an unmarshal
	// error and writing it back would destroy every non-mcp key the user has
	// (model, theme, keybinds, ...). A transient/partial write or a
	// hand-edited-with-comments file (opencode tolerates JSONC in practice)
	// would otherwise trigger total config loss. Only initialize an empty config
	// when the file genuinely does not exist.
	var rawConfig map[string]interface{}
	if data, readErr := os.ReadFile(mcpFile); readErr == nil {
		if jsonErr := json.Unmarshal(data, &rawConfig); jsonErr != nil {
			return fmt.Errorf("refusing to overwrite unparseable opencode project config %s: %w", mcpFile, jsonErr)
		}
		if rawConfig == nil { // a JSON null root decodes to a nil map
			rawConfig = make(map[string]interface{})
		}
	} else if os.IsNotExist(readErr) {
		rawConfig = make(map[string]interface{})
	} else {
		return fmt.Errorf("read opencode project mcp %s: %w", mcpFile, readErr)
	}

	setOpenCodeMCPServers(rawConfig, buildOpenCodeMCPServers(enabledNames), len(nativeV2) > 0 && nativeV2[0])

	newData, err := json.MarshalIndent(rawConfig, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal opencode project mcp: %w", err)
	}
	if err := atomicfile.WriteFileWithin(filepath.Dir(mcpFile), mcpFile, newData, 0o600); err != nil {
		return fmt.Errorf("save opencode project mcp: %w", withProjectMCPOptOut(err))
	}
	excludeProjectMCPFromGit(projectPath, mcpFile)

	ClearOpenCodeMCPCache(projectPath)
	return nil
}

// WriteOpenCodeGlobalMCP writes catalog MCPs to ~/.config/opencode/opencode.json.
// Preserves other JSON keys already present in the file.
func WriteOpenCodeGlobalMCP(enabledNames []string, nativeV2 ...bool) error {
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

	// Fail closed on an existing-but-unparseable file — see the rationale in
	// WriteOpenCodeProjectMCP. Overwriting it would drop every non-mcp key.
	var rawConfig map[string]interface{}
	if data, readErr := os.ReadFile(configFile); readErr == nil {
		if jsonErr := json.Unmarshal(data, &rawConfig); jsonErr != nil {
			return fmt.Errorf("refusing to overwrite unparseable opencode global config %s: %w", configFile, jsonErr)
		}
		if rawConfig == nil { // a JSON null root decodes to a nil map
			rawConfig = make(map[string]interface{})
		}
	} else if os.IsNotExist(readErr) {
		rawConfig = make(map[string]interface{})
	} else {
		return fmt.Errorf("read opencode global mcp %s: %w", configFile, readErr)
	}

	setOpenCodeMCPServers(rawConfig, buildOpenCodeMCPServers(enabledNames), len(nativeV2) > 0 && nativeV2[0])

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
