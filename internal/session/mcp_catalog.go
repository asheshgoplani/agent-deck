package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/mcppool"
	"github.com/asheshgoplani/agent-deck/internal/safeio"
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
			if !config.MCPPool.GetFallbackStdio() {
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
//
// Returns (nil, nil) ONLY when the file genuinely does not exist. A file that
// exists but cannot be read or parsed returns an error.
//
// This is the same defect readJSONObjectConfig fixed for .claude.json, in the
// fifth writer path (#1956). It returned nil for a read error and for a parse
// error alike; the caller merges that nil into a fresh map and writes the whole
// file back, so a transient I/O failure or a half-written .mcp.json silently
// deleted every server the user had declared there. .mcp.json is a file the
// user owns and commits, so the loss lands in their repository.
//
// "I could not read it" is never "there is nothing there".
func readExistingLocalMCPServers(mcpFile string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(mcpFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("refusing to modify %s: it could not be read (%w). "+
			"Writing would replace its current contents — fix or restore the file first", mcpFile, err)
	}
	var config struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("refusing to modify %s: it is not valid JSON (%w). "+
			"Writing would replace its current contents — fix or restore the file first", mcpFile, err)
	}
	return config.MCPServers, nil
}

// writeJSONFileAtomic writes JSON data to path atomically (symlink-preserving,
// via atomicfile.WriteFile), guaranteeing exactly one trailing newline and
// skipping the write entirely when the on-disk bytes already match.
// json.MarshalIndent emits no trailing newline, so without this a restart
// rewrote a byte-identical-except-for-the-missing-\n file, leaving a persistent
// one-byte git diff (issue #1627). Skipping the atomic replace when nothing
// changed also avoids needless filesystem churn.
func writeJSONFileAtomic(path string, data []byte, perm os.FileMode) error {
	if len(data) == 0 || data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) {
		return nil
	}
	return atomicfile.WriteFile(path, data, perm)
}

// WriteMergedMcpJSONFile writes enabled MCPs from config.toml to mcpFile using the
// Claude/Cursor JSON shape {"mcpServers":{...}}. It preserves entries not defined in
// config.toml. When pluginPinClaudeProfile is non-empty (Claude project .mcp.json),
// refreshes stale plugin version pins before merging (#960).
func WriteMergedMcpJSONFile(mcpFile string, enabledNames []string, pluginPinClaudeProfile string) error {
	// Same read-modify-write class as the .claude.json writers: this reads the
	// existing servers, merges, and writes the whole file back. The web local
	// scope and the TUI both reach it, so it takes the same per-path lock.
	lock, lockErr := AcquireConfigFileLock(mcpFile)
	if lockErr != nil {
		return lockErr
	}
	defer lock.Release()

	availableMCPs := GetAvailableMCPs()
	pool := GetGlobalPool()

	if pluginPinClaudeProfile != "" {
		if _, err := RefreshStalePluginPins(mcpFile, []string{pluginPinClaudeProfile}); err != nil {
			mcpCatLog.Warn("plugin_pin_refresh_failed", "path", mcpFile, "error", err)
		}
	}

	existingServers, err := readExistingLocalMCPServers(mcpFile)
	if err != nil {
		return err
	}
	agentDeckServers := make(map[string]MCPServerConfig)

	for _, name := range enabledNames {
		if def, ok := availableMCPs[name]; ok {
			if def.URL != "" {
				if def.HasAutoStartServer() {
					if err := StartHTTPServer(name, &def); err != nil {
						mcpCatLog.Warn("http_server_start_failed", slog.String("mcp", name), slog.String("scope", "local"), slog.Any("error", err))
					}
				}

				transport := def.Transport
				if transport == "" {
					transport = "http"
				}
				agentDeckServers[name] = MCPServerConfig{
					Type:    transport,
					URL:     def.URL,
					Headers: def.Headers,
				}
				mcpCatLog.Info("transport_http", slog.String("mcp", name), slog.String("scope", "local"), slog.String("transport", transport), slog.String("url", def.URL))
				continue
			}

			if socketCfg, used := tryPoolSocket(pool, name, "local"); used {
				agentDeckServers[name] = socketCfg
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
			agentDeckServers[name] = MCPServerConfig{
				Type:    "stdio",
				Command: def.Command,
				Args:    args,
				Env:     env,
			}
			mcpCatLog.Info("transport_stdio", slog.String("mcp", name), slog.String("scope", "local"))
		}
	}

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
		return fmt.Errorf("failed to marshal mcp json: %w", err)
	}

	if err := writeJSONFileAtomic(mcpFile, data, 0644); err != nil {
		return fmt.Errorf("failed to save mcp json: %w", err)
	}

	return nil
}

// WriteMCPJsonFromConfig writes enabled MCPs from config.toml to project's .mcp.json
// It preserves any existing entries not managed by agent-deck (not defined in config.toml)
func WriteMCPJsonFromConfig(projectPath string, enabledNames []string) error {
	if !GetManageMCPJson() {
		mcpCatLog.Debug("mcp_json_management_disabled", slog.String("path", projectPath))
		return nil
	}

	mcpFile := filepath.Join(projectPath, ".mcp.json")
	return WriteMergedMcpJSONFile(mcpFile, enabledNames, GetClaudeConfigDir())
}

// ---------------------------------------------------------------------------
// Fail-closed reading and writing of Claude-style JSON configs.
//
// .claude.json holds the user's entire Claude configuration: settings, every
// project entry, and the root mcpServers map. The MCP writers below are
// read-modify-write, so a parse failure that degrades to an empty map does not
// merely lose the MCP list — the next write persists that empty map and the
// whole file is gone. A transiently malformed file (a half-finished manual
// edit, a truncated write from another process) is exactly when this triggers.
//
// HARD RULE: a config derived from a failed parse is never written. A parse
// failure aborts the mutation and names the problem. Only a missing or empty
// file legitimately starts from a fresh map.
// ---------------------------------------------------------------------------

// readJSONObjectConfig loads a JSON object config, failing closed on a parse
// error rather than substituting an empty map.
func readJSONObjectConfig(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]interface{}{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]interface{}{}, nil
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("refusing to modify %s: it is not valid JSON (%w). "+
			"Writing would replace its current contents — fix or restore the file first", path, err)
	}
	if cfg == nil {
		// json.Unmarshal("null", &map) SUCCEEDS and yields a nil map, so the
		// parse check above never fires for a `null` root. Starting fresh here
		// would replace the file — the same substitution this function exists to
		// prevent, reached by the one input that walks past the guard. A `null`
		// document holds no user data, so nothing is lost by refusing either.
		return nil, fmt.Errorf("refusing to modify %s: its JSON root is null, not an object. "+
			"Writing would replace its current contents — fix or restore the file first", path)
	}
	return cfg, nil
}

// objectFieldForUpdate returns obj[key] as a JSON object for merging.
//
// Absent and explicit-null both mean "nothing to preserve" and yield a fresh
// empty map. A value of any other type means the document is not shaped the way
// this writer assumes, and replacing it would discard whatever it holds.
//
// This is the case refuseDroppingTopLevelKeys cannot see. That guard compares
// top-level KEY SETS, so rewriting the VALUE of a key present in both documents
// reads as nothing dropped — while every entry underneath it is gone. A
// `.claude.json` whose "projects" value is corrupt kept "projects" as a key and
// lost all three project entries, reported as success (#1956).
func objectFieldForUpdate(obj map[string]interface{}, key, describe string) (map[string]interface{}, error) {
	v, present := obj[key]
	if !present || v == nil {
		return make(map[string]interface{}), nil
	}
	m, ok := v.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("refusing to modify %s: it is %T, not a JSON object. "+
			"Writing would discard its current contents — fix or restore the file first", describe, v)
	}
	return m, nil
}

// refuseDroppingTopLevelKeys is the second net under readJSONObjectConfig: even
// if some future path reconstructs a config from nothing, the write is refused
// when it would remove top-level keys the file already has. Every MCP writer
// here only ever sets "mcpServers" or "projects" on a config it just read, so a
// dropped key means the read did not happen.
func refuseDroppingTopLevelKeys(path string) func(old, updated []byte) error {
	return func(old, updated []byte) error {
		if len(bytes.TrimSpace(old)) == 0 {
			return nil
		}
		var oldDoc map[string]json.RawMessage
		if err := json.Unmarshal(old, &oldDoc); err != nil {
			return fmt.Errorf("refusing to overwrite %s: the file on disk is not valid JSON (%w)", path, err)
		}
		var newDoc map[string]json.RawMessage
		if err := json.Unmarshal(updated, &newDoc); err != nil {
			return fmt.Errorf("refusing to write %s: generated content is not valid JSON (%w)", path, err)
		}
		var dropped []string
		for k := range oldDoc {
			if _, ok := newDoc[k]; !ok {
				dropped = append(dropped, k)
			}
		}
		if len(dropped) > 0 {
			sort.Strings(dropped)
			return fmt.Errorf("refusing to write %s: it would drop top-level keys %v that are on disk", path, dropped)
		}
		return nil
	}
}

// writeJSONObjectConfig marshals cfg and writes it through safeio, which keeps
// a .bak, refuses an empty payload over a populated file, and runs the
// drop-a-key guard before touching anything.
func writeJSONObjectConfig(path string, cfg map[string]interface{}) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	return safeio.SafeOverwrite(path, data, safeio.Options{
		Perm:        0600,
		RefuseEmpty: true,
		Guard:       refuseDroppingTopLevelKeys(path),
	})
}

// WriteGlobalMCP adds or removes MCPs from Claude's global config
// This modifies ~/.claude-work/.claude.json → mcpServers
func writeGlobalMCPLocked(enabledNames []string) error {
	configDir := GetClaudeConfigDir()
	configFile := filepath.Join(configDir, ".claude.json")

	// Read existing config (preserve other fields like projects, settings, etc.)
	// A parse failure aborts the mutation: degrading to an empty map here and
	// writing it back would delete the user's entire Claude configuration.
	rawConfig, err := readJSONObjectConfig(configFile)
	if err != nil {
		return err
	}

	availableMCPs := GetAvailableMCPs()
	mcpServers := buildManagedMCPServers(enabledNames, "global")

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

	return writeJSONObjectConfig(configFile, rawConfig)
}

// ---------------------------------------------------------------------------
// Public writers. Each takes the shared config-file lock for the WHOLE
// read-modify-write cycle, then delegates to the *Locked body.
//
// The lock is not reentrant, so anything needing several writes under one lock
// calls the *Locked variants directly after acquiring once — see
// WriteGlobalMCPAndClearProjectMCPs.
// ---------------------------------------------------------------------------

func claudeConfigFilePath() string {
	return filepath.Join(GetClaudeConfigDir(), ".claude.json")
}

// WriteGlobalMCP adds or removes MCPs from Claude's global config
// (CLAUDE_CONFIG_DIR/.claude.json → mcpServers).
func WriteGlobalMCP(enabledNames []string) error {
	lock, err := AcquireConfigFileLock(claudeConfigFilePath())
	if err != nil {
		return err
	}
	defer lock.Release()
	return writeGlobalMCPLocked(enabledNames)
}

// WriteProjectMCP writes enabled catalog MCPs into
// projects[projectPath].mcpServers of Claude's config.
func WriteProjectMCP(projectPath string, enabledNames []string) error {
	lock, err := AcquireConfigFileLock(claudeConfigFilePath())
	if err != nil {
		return err
	}
	defer lock.Release()
	return writeProjectMCPLocked(projectPath, enabledNames)
}

// WriteUserMCP writes MCPs to ~/.claude.json (the ROOT config Claude always
// reads). This is a different file from the CLAUDE_CONFIG_DIR one, so it takes
// its own lock.
func WriteUserMCP(enabledNames []string) error {
	lock, err := AcquireConfigFileLock(GetUserMCPRootPath())
	if err != nil {
		return err
	}
	defer lock.Release()
	return writeUserMCPLocked(enabledNames)
}

// ClearProjectMCPs removes all MCPs from projects[path].mcpServers in Claude's
// config.
func ClearProjectMCPs(projectPath string) error {
	lock, err := AcquireConfigFileLock(claudeConfigFilePath())
	if err != nil {
		return err
	}
	defer lock.Release()
	return clearProjectMCPsLocked(projectPath)
}

// WriteGlobalMCPAndClearProjectMCPs performs the TUI's global-apply as ONE
// serialized unit: rewrite the root mcpServers map, then clear the project half
// that the global view merges in.
//
// These are two writes to the same file that must not be interleaved with any
// other writer. Done as separate public calls they take and drop the lock
// twice, leaving a window where another process sees the config with the new
// global set but the stale project set still merged in — which reads as MCPs
// the user just removed still being attached.
func WriteGlobalMCPAndClearProjectMCPs(projectPath string, enabledNames []string) error {
	lock, err := AcquireConfigFileLock(claudeConfigFilePath())
	if err != nil {
		return err
	}
	defer lock.Release()
	if err := writeGlobalMCPLocked(enabledNames); err != nil {
		return err
	}
	return clearProjectMCPsLocked(projectPath)
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

// buildManagedMCPServers turns catalog names into the mcpServers entries that
// go into a Claude-style config, honouring HTTP/SSE definitions and the shared
// stdio pool. scope is used only for log attribution.
func buildManagedMCPServers(enabledNames []string, scope string) map[string]MCPServerConfig {
	availableMCPs := GetAvailableMCPs()
	pool := GetGlobalPool() // may be nil
	mcpServers := make(map[string]MCPServerConfig)

	for _, name := range enabledNames {
		def, ok := availableMCPs[name]
		if !ok {
			continue
		}
		if def.URL != "" {
			if def.HasAutoStartServer() {
				if err := StartHTTPServer(name, &def); err != nil {
					mcpCatLog.Warn("http_server_start_failed", slog.String("mcp", name), slog.String("scope", scope), slog.Any("error", err))
				}
			}
			transport := def.Transport
			if transport == "" {
				transport = "http"
			}
			mcpServers[name] = MCPServerConfig{Type: transport, URL: def.URL, Headers: def.Headers}
			mcpCatLog.Info("transport_http", slog.String("mcp", name), slog.String("scope", scope), slog.String("transport", transport), slog.String("url", def.URL))
			continue
		}
		if socketCfg, used := tryPoolSocket(pool, name, scope); used {
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
		mcpServers[name] = MCPServerConfig{Type: "stdio", Command: def.Command, Args: args, Env: env}
		mcpCatLog.Info("transport_stdio", slog.String("mcp", name), slog.String("scope", scope))
	}
	return mcpServers
}

// WriteProjectMCP writes enabled catalog MCPs into
// projects[projectPath].mcpServers of Claude's config.
//
// This is the write counterpart to GetProjectMCPNames. Claude keeps
// per-project servers there, distinct from the root mcpServers map that
// WriteGlobalMCP owns and from the project's own .mcp.json. Without this,
// anything reporting project entries had to write them through the root map,
// which silently moved servers between scopes.
//
// Entries not defined in config.toml are preserved (#146).
func writeProjectMCPLocked(projectPath string, enabledNames []string) error {
	if projectPath == "" {
		return fmt.Errorf("project MCP write: empty project path")
	}
	configDir := GetClaudeConfigDir()
	configFile := filepath.Join(configDir, ".claude.json")

	// A parse failure aborts the mutation: degrading to an empty map here and
	// writing it back would delete the user's entire Claude configuration.
	rawConfig, err := readJSONObjectConfig(configFile)
	if err != nil {
		return err
	}

	projects, err := objectFieldForUpdate(rawConfig, "projects", fmt.Sprintf("%s: projects", configFile))
	if err != nil {
		return err
	}
	proj, err := objectFieldForUpdate(projects, projectPath, fmt.Sprintf("%s: projects[%q]", configFile, projectPath))
	if err != nil {
		return err
	}
	existing, err := objectFieldForUpdate(proj, "mcpServers", fmt.Sprintf("%s: projects[%q].mcpServers", configFile, projectPath))
	if err != nil {
		return err
	}

	availableMCPs := GetAvailableMCPs()
	managed := buildManagedMCPServers(enabledNames, "project")

	merged := make(map[string]interface{})
	for name, cfg := range existing {
		if _, isManaged := availableMCPs[name]; !isManaged {
			merged[name] = cfg
		}
	}
	for name, cfg := range managed {
		merged[name] = cfg
	}

	proj["mcpServers"] = merged
	projects[projectPath] = proj
	rawConfig["projects"] = projects

	return writeJSONObjectConfig(configFile, rawConfig)
}

// ClearProjectMCPs removes all MCPs from projects[path].mcpServers in Claude's config
func clearProjectMCPsLocked(projectPath string) error {
	configFile := claudeConfigFilePath()

	// Through the shared reader, not a hand-rolled copy. The inline version
	// this replaces failed closed on a bad parse (correct) but ALSO errored on
	// a genuinely absent file — the inverted bug: nothing to clear is success,
	// not failure. readJSONObjectConfig draws that line once, for every caller.
	rawConfig, err := readJSONObjectConfig(configFile)
	if err != nil {
		return err
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

	return writeJSONObjectConfig(configFile, rawConfig)
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
func writeUserMCPLocked(enabledNames []string) error {
	configFile := GetUserMCPRootPath()
	if configFile == "" {
		return fmt.Errorf("could not determine home directory")
	}

	// Read existing config (preserve other fields like numStartups, projects, etc.)
	// A parse failure aborts the mutation: degrading to an empty map here and
	// writing it back would delete the user's entire Claude configuration.
	rawConfig, err := readJSONObjectConfig(configFile)
	if err != nil {
		return err
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

	return writeJSONObjectConfig(configFile, rawConfig)
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

// RefuseDroppingTopLevelKeysForTest exposes the safeio guard so tests in other
// packages can assert the second net directly. Not used in production paths.
func RefuseDroppingTopLevelKeysForTest(path string) func(old, updated []byte) error {
	return refuseDroppingTopLevelKeys(path)
}
