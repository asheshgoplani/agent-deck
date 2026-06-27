package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
)

// codexMCPServer is the [mcp_servers.NAME] shape in $CODEX_HOME/config.toml.
type codexMCPServer struct {
	Command           string            `toml:"command,omitempty"`
	Args              []string          `toml:"args,omitempty"`
	Env               map[string]string `toml:"env,omitempty"`
	URL               string            `toml:"url,omitempty"`
	BearerTokenEnvVar string            `toml:"bearer_token_env_var,omitempty"`
	HTTPHeaders       map[string]string `toml:"http_headers,omitempty"`
}

type codexMCPConfig struct {
	MCPServers map[string]codexMCPServer `toml:"mcp_servers,omitempty"`
}

var (
	codexMcpInfoCache   = make(map[string]*MCPInfo)
	codexMcpInfoCacheMu sync.RWMutex
	codexMcpCacheTimes  = make(map[string]time.Time)
)

// GetCodexConfigDir returns the Codex home directory used for config.toml.
func GetCodexConfigDir() string {
	return getCodexHomeDir()
}

func codexConfigPath(codexHome string) string {
	codexHome = strings.TrimSpace(codexHome)
	if codexHome == "" {
		return ""
	}
	if cleaned := filepath.Clean(codexHome); cleaned != "." && cleaned != "" {
		return filepath.Join(cleaned, "config.toml")
	}
	return ""
}

// GetCodexMCPInfo reads Codex MCP config from $CODEX_HOME/config.toml.
func GetCodexMCPInfo(codexHome string) *MCPInfo {
	if codexHome == "" {
		codexHome = getCodexHomeDir()
	}

	codexMcpInfoCacheMu.RLock()
	if cached, ok := codexMcpInfoCache[codexHome]; ok {
		if time.Since(codexMcpCacheTimes[codexHome]) < mcpCacheExpiry {
			codexMcpInfoCacheMu.RUnlock()
			return cached
		}
	}
	codexMcpInfoCacheMu.RUnlock()

	info := getCodexMCPInfoUncached(codexHome)

	codexMcpInfoCacheMu.Lock()
	codexMcpInfoCache[codexHome] = info
	codexMcpCacheTimes[codexHome] = time.Now()
	codexMcpInfoCacheMu.Unlock()

	return info
}

func getCodexMCPInfoUncached(codexHome string) *MCPInfo {
	info := &MCPInfo{}
	configFile := codexConfigPath(codexHome)
	if configFile == "" {
		return info
	}

	var cfg codexMCPConfig
	if _, err := toml.DecodeFile(configFile, &cfg); err != nil {
		return info
	}

	for name := range cfg.MCPServers {
		info.Global = append(info.Global, name)
	}
	sort.Strings(info.Global)
	return info
}

// ClearCodexMCPCache invalidates cached Codex MCP info for a Codex home.
func ClearCodexMCPCache(codexHome string) {
	if codexHome == "" {
		codexHome = getCodexHomeDir()
	}
	codexMcpInfoCacheMu.Lock()
	defer codexMcpInfoCacheMu.Unlock()
	delete(codexMcpInfoCache, codexHome)
	delete(codexMcpCacheTimes, codexHome)
}

// ClearAllCodexMCPInfoCache clears all Codex MCP cache entries.
func ClearAllCodexMCPInfoCache() {
	codexMcpInfoCacheMu.Lock()
	defer codexMcpInfoCacheMu.Unlock()
	clear(codexMcpInfoCache)
	clear(codexMcpCacheTimes)
}

// PruneCodexMCPCache removes stale Codex MCP cache entries.
func PruneCodexMCPCache(maxAge time.Duration) {
	codexMcpInfoCacheMu.Lock()
	defer codexMcpInfoCacheMu.Unlock()
	now := time.Now()
	for path, t := range codexMcpCacheTimes {
		if now.Sub(t) > maxAge {
			delete(codexMcpInfoCache, path)
			delete(codexMcpCacheTimes, path)
		}
	}
}

func buildCodexMCPServers(enabledNames []string) map[string]codexMCPServer {
	availableMCPs := GetAvailableMCPs()
	pool := GetGlobalPool()

	servers := make(map[string]codexMCPServer)
	for _, name := range enabledNames {
		def, ok := availableMCPs[name]
		if !ok {
			continue
		}

		if def.URL != "" {
			if def.HasAutoStartServer() {
				if err := StartHTTPServer(name, &def); err != nil {
					mcpCatLog.Warn("http_server_start_failed_codex", "mcp", name, "error", err)
				}
			}
			servers[name] = codexMCPServer{
				URL:         def.URL,
				HTTPHeaders: def.Headers,
			}
			continue
		}

		if socketCfg, used := tryPoolSocket(pool, name, "codex"); used {
			servers[name] = codexMCPServer{
				Command: socketCfg.Command,
				Args:    socketCfg.Args,
				Env:     socketCfg.Env,
			}
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
		servers[name] = codexMCPServer{
			Command: def.Command,
			Args:    args,
			Env:     env,
		}
	}
	return servers
}

func mergeCodexMCPServers(enabledNames []string, existing map[string]codexMCPServer) map[string]codexMCPServer {
	availableMCPs := GetAvailableMCPs()
	servers := make(map[string]codexMCPServer)
	for name, server := range existing {
		if _, managed := availableMCPs[name]; !managed {
			servers[name] = server
		}
	}
	for name, server := range buildCodexMCPServers(enabledNames) {
		servers[name] = server
	}
	return servers
}

func tomlTableName(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "[[") {
		end := strings.Index(line, "]]")
		if end < 0 {
			return "", false
		}
		return strings.TrimSpace(line[2:end]), true
	}
	if strings.HasPrefix(line, "[") {
		end := strings.Index(line, "]")
		if end < 0 {
			return "", false
		}
		return strings.TrimSpace(line[1:end]), true
	}
	return "", false
}

func stripCodexMCPSections(data []byte) string {
	var out strings.Builder
	inMCPSection := false
	for _, line := range strings.SplitAfter(string(data), "\n") {
		if tableName, ok := tomlTableName(line); ok {
			inMCPSection = tableName == "mcp_servers" || strings.HasPrefix(tableName, "mcp_servers.")
		}
		if inMCPSection {
			continue
		}
		out.WriteString(line)
	}
	return strings.TrimRight(out.String(), "\n")
}

func encodeCodexMCPSections(servers map[string]codexMCPServer) (string, error) {
	if len(servers) == 0 {
		return "", nil
	}
	var out strings.Builder
	if err := toml.NewEncoder(&out).Encode(codexMCPConfig{MCPServers: servers}); err != nil {
		return "", err
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

func composeCodexConfig(existingData []byte, servers map[string]codexMCPServer) ([]byte, error) {
	base := stripCodexMCPSections(existingData)
	mcpSections, err := encodeCodexMCPSections(servers)
	if err != nil {
		return nil, err
	}

	switch {
	case base == "" && mcpSections == "":
		return []byte{}, nil
	case base == "":
		return []byte(mcpSections + "\n"), nil
	case mcpSections == "":
		return []byte(base + "\n"), nil
	default:
		return []byte(base + "\n\n" + mcpSections + "\n"), nil
	}
}

// WriteCodexMCPConfig writes catalog MCPs to $CODEX_HOME/config.toml.
// Existing non-catalog [mcp_servers.*] tables and non-MCP Codex config text are preserved.
func WriteCodexMCPConfig(codexHome string, enabledNames []string) error {
	if codexHome == "" {
		codexHome = getCodexHomeDir()
	}
	configFile := codexConfigPath(codexHome)
	if configFile == "" {
		return fmt.Errorf("cannot resolve Codex config dir")
	}
	if err := os.MkdirAll(filepath.Dir(configFile), 0o755); err != nil {
		return fmt.Errorf("create Codex config dir: %w", err)
	}

	var existingData []byte
	var existingConfig codexMCPConfig
	if _, err := os.Stat(configFile); err == nil {
		var readErr error
		existingData, readErr = os.ReadFile(configFile)
		if readErr != nil {
			return fmt.Errorf("read Codex config %s: %w", configFile, readErr)
		}
		if _, err := toml.Decode(string(existingData), &existingConfig); err != nil {
			return fmt.Errorf("refusing to overwrite unparseable Codex config %s: %w", configFile, err)
		}
	} else if os.IsNotExist(err) {
		existingConfig.MCPServers = make(map[string]codexMCPServer)
	} else {
		return fmt.Errorf("read Codex config %s: %w", configFile, err)
	}

	servers := mergeCodexMCPServers(enabledNames, existingConfig.MCPServers)
	data, err := composeCodexConfig(existingData, servers)
	if err != nil {
		return fmt.Errorf("marshal Codex config: %w", err)
	}
	if err := atomicfile.WriteFile(configFile, data, 0o600); err != nil {
		return fmt.Errorf("save Codex config: %w", err)
	}

	ClearCodexMCPCache(codexHome)
	return nil
}
