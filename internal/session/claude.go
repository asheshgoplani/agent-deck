package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// claudeDirNameRegex matches any character that's not alphanumeric or hyphen
// Claude Code replaces all such characters with hyphens in project directory names
var claudeDirNameRegex = regexp.MustCompile(`[^a-zA-Z0-9-]`)

// ConvertToClaudeDirName converts a filesystem path to Claude's directory naming format.
// Claude Code replaces all non-alphanumeric characters (except hyphens) with hyphens.
// Example: /Users/master/Code cloud/!Project → -Users-master-Code-cloud--Project
func ConvertToClaudeDirName(path string) string {
	return claudeDirNameRegex.ReplaceAllString(path, "-")
}

// ClaudeProject represents a project entry in Claude's config
type ClaudeProject struct {
	LastSessionId string `json:"lastSessionId"`
}

// ClaudeConfig represents the structure of .claude.json
type ClaudeConfig struct {
	Projects map[string]ClaudeProject `json:"projects"`
}

// LocalMCP represents an MCP defined in a local .mcp.json file
type LocalMCP struct {
	Name       string // MCP name
	SourcePath string // Directory containing the .mcp.json file
}

// MCPInfo contains MCP server information for a session
type MCPInfo struct {
	Global    []string   // From CLAUDE_CONFIG_DIR/.claude.json mcpServers
	Project   []string   // From CLAUDE_CONFIG_DIR/.claude.json projects[path].mcpServers
	LocalMCPs []LocalMCP // From .mcp.json files (walks up parent directories)
}

// Local returns MCP names for backward compatibility
// Use LocalMCPs directly if you need source path information
func (m *MCPInfo) Local() []string {
	names := make([]string, len(m.LocalMCPs))
	for i, mcp := range m.LocalMCPs {
		names[i] = mcp.Name
	}
	return names
}

// HasAny returns true if any MCPs are configured
func (m *MCPInfo) HasAny() bool {
	return len(m.Global) > 0 || len(m.Project) > 0 || len(m.LocalMCPs) > 0
}

// Total returns total number of MCPs across all sources
func (m *MCPInfo) Total() int {
	return len(m.Global) + len(m.Project) + len(m.LocalMCPs)
}

// AllNames returns a deduplicated, sorted list of all MCP names across all sources
// Used for capturing loaded MCPs at session start for sync tracking
func (m *MCPInfo) AllNames() []string {
	seen := make(map[string]bool)
	for _, name := range m.Global {
		seen[name] = true
	}
	for _, name := range m.Project {
		seen[name] = true
	}
	for _, mcp := range m.LocalMCPs {
		seen[mcp.Name] = true
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// claudeConfigForMCP is used for parsing MCP-related fields from .claude.json
type claudeConfigForMCP struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
	Projects   map[string]struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	} `json:"projects"`
}

// projectMCPConfig is used for parsing .mcp.json files
type projectMCPConfig struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
}

// MCP info cache (30 second TTL to avoid re-reading files on every render)
var (
	mcpInfoCache   = make(map[string]*MCPInfo)
	mcpInfoCacheMu sync.RWMutex
	mcpCacheExpiry = 30 * time.Second
	mcpCacheTimes  = make(map[string]time.Time)
)

// MCPServer represents an MCP with its enabled state
type MCPServer struct {
	Name    string
	Source  string // "local", "global", "project"
	Enabled bool
}

// ProjectMCPSettings represents .claude/settings.local.json
type ProjectMCPSettings struct {
	EnableAllProjectMcpServers bool     `json:"enableAllProjectMcpServers,omitempty"`
	EnabledMcpjsonServers      []string `json:"enabledMcpjsonServers,omitempty"`
	DisabledMcpjsonServers     []string `json:"disabledMcpjsonServers,omitempty"`
}

// MCPMode indicates how MCP enabling/disabling is configured
type MCPMode int

const (
	MCPModeDefault   MCPMode = iota // No explicit config, all enabled
	MCPModeWhitelist                // enabledMcpjsonServers is set
	MCPModeBlacklist                // disabledMcpjsonServers is set
)

// GetMCPInfo retrieves MCP server information for a project path (cached)
// It reads from three sources:
// 1. Global MCPs: CLAUDE_CONFIG_DIR/.claude.json → mcpServers
// 2. Project MCPs: CLAUDE_CONFIG_DIR/.claude.json → projects[projectPath].mcpServers
// 3. Local MCPs: {projectPath}/.mcp.json → mcpServers
func GetMCPInfo(projectPath string) *MCPInfo {
	// Check cache first
	mcpInfoCacheMu.RLock()
	if cached, ok := mcpInfoCache[projectPath]; ok {
		if time.Since(mcpCacheTimes[projectPath]) < mcpCacheExpiry {
			mcpInfoCacheMu.RUnlock()
			return cached
		}
	}
	mcpInfoCacheMu.RUnlock()

	// Cache miss or expired - fetch fresh data
	info := getMCPInfoUncached(projectPath)

	// Update cache
	mcpInfoCacheMu.Lock()
	mcpInfoCache[projectPath] = info
	mcpCacheTimes[projectPath] = time.Now()
	mcpInfoCacheMu.Unlock()

	return info
}

// getMCPInfoUncached reads MCP info from disk (called by cached wrapper)
func getMCPInfoUncached(projectPath string) *MCPInfo {
	info := &MCPInfo{}
	configDir := GetClaudeConfigDir()

	// Read .claude.json for global and project MCPs
	configFile := filepath.Join(configDir, ".claude.json")
	if data, err := os.ReadFile(configFile); err == nil {
		var config claudeConfigForMCP
		if json.Unmarshal(data, &config) == nil {
			// Global MCPs
			for name := range config.MCPServers {
				info.Global = append(info.Global, name)
			}
			// Project-specific MCPs
			if proj, ok := config.Projects[projectPath]; ok {
				for name := range proj.MCPServers {
					info.Project = append(info.Project, name)
				}
			}
		}
	}

	// Read .mcp.json from project directory for local MCPs
	// Walk up parent directories to find .mcp.json (matches Claude Code behavior)
	currentPath := projectPath
	for {
		mcpFile := filepath.Join(currentPath, ".mcp.json")
		if data, err := os.ReadFile(mcpFile); err == nil {
			var mcp projectMCPConfig
			if json.Unmarshal(data, &mcp) == nil {
				for name := range mcp.MCPServers {
					info.LocalMCPs = append(info.LocalMCPs, LocalMCP{
						Name:       name,
						SourcePath: currentPath,
					})
				}
			}
			break // Stop at first .mcp.json found
		}

		// Move up to parent directory
		parent := filepath.Dir(currentPath)
		if parent == currentPath || parent == "/" || parent == "." {
			break // Reached root or invalid path
		}
		currentPath = parent
	}

	// Sort for consistent display
	sort.Strings(info.Global)
	sort.Strings(info.Project)
	// Sort LocalMCPs by name
	sort.Slice(info.LocalMCPs, func(i, j int) bool {
		return info.LocalMCPs[i].Name < info.LocalMCPs[j].Name
	})

	return info
}

// GetClaudeConfigDir returns the Claude config directory
// Priority: 1) CLAUDE_CONFIG_DIR env, 2) UserConfig setting, 3) ~/.claude
func GetClaudeConfigDir() string {
	// 1. Check env var (highest priority)
	if envDir := os.Getenv("CLAUDE_CONFIG_DIR"); envDir != "" {
		return expandTilde(envDir)
	}

	// 2. Check user config
	userConfig, _ := LoadUserConfig()
	if userConfig != nil && userConfig.Claude.ConfigDir != "" {
		return expandTilde(userConfig.Claude.ConfigDir)
	}

	// 3. Default to ~/.claude
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// IsClaudeConfigDirExplicit returns true if the Claude config directory is
// explicitly configured (via CLAUDE_CONFIG_DIR env var or config.toml setting).
// When false, the user is using the default path and we should NOT override
// CLAUDE_CONFIG_DIR in commands, allowing the shell's environment to be respected.
//
// This is critical for WSL and other environments where users may have
// CLAUDE_CONFIG_DIR set in their .bashrc/.zshrc - agent-deck should not
// override that with a hardcoded default path.
func IsClaudeConfigDirExplicit() bool {
	// Check env var
	if os.Getenv("CLAUDE_CONFIG_DIR") != "" {
		return true
	}

	// Check user config
	userConfig, _ := LoadUserConfig()
	if userConfig != nil && userConfig.Claude.ConfigDir != "" {
		return true
	}

	return false
}

// GetClaudeCommand returns the configured Claude command/alias
// Priority: 1) UserConfig setting, 2) Default "claude"
// This allows users to configure an alias like "cdw" or "cdp" that sets
// CLAUDE_CONFIG_DIR automatically, avoiding the need for config_dir setting
func GetClaudeCommand() string {
	userConfig, _ := LoadUserConfig()
	if userConfig != nil && userConfig.Claude.Command != "" {
		return userConfig.Claude.Command
	}
	return "claude"
}

// GetClaudeSessionID returns the ACTIVE session ID for a project path
// It first tries to find the currently running session by checking recently
// modified .jsonl files, then falls back to lastSessionId from config
func GetClaudeSessionID(projectPath string) (string, error) {
	configDir := GetClaudeConfigDir()

	// First, try to find active session from recently modified files
	activeID := findActiveSessionID(configDir, projectPath)
	if activeID != "" {
		return activeID, nil
	}

	// Fall back to lastSessionId from config
	configFile := filepath.Join(configDir, ".claude.json")
	data, err := os.ReadFile(configFile)
	if err != nil {
		return "", fmt.Errorf("failed to read Claude config: %w", err)
	}

	var config ClaudeConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return "", fmt.Errorf("failed to parse Claude config: %w", err)
	}

	// Look up project by path
	if project, ok := config.Projects[projectPath]; ok {
		if project.LastSessionId != "" {
			return project.LastSessionId, nil
		}
	}

	return "", fmt.Errorf("no session found for project: %s", projectPath)
}

// findActiveSessionID looks for the most recently modified session file
// This finds the CURRENTLY RUNNING session, not the last completed one
func findActiveSessionID(configDir, projectPath string) string {
	return findActiveSessionIDExcluding(configDir, projectPath, nil)
}

// findActiveSessionIDExcluding looks for the most recently modified session file,
// skipping any session IDs in the exclude set. This prevents picking up a .jsonl
// owned by another agent-deck instance when multiple sessions share the same project.
func findActiveSessionIDExcluding(configDir, projectPath string, excludeIDs map[string]bool) string {
	// Convert project path to Claude's directory format
	// Claude replaces ALL non-alphanumeric chars (spaces, !, etc.) with hyphens
	// /Users/master/Code cloud/!Project -> -Users-master-Code-cloud--Project
	projectDirName := ConvertToClaudeDirName(projectPath)
	projectDir := filepath.Join(configDir, "projects", projectDirName)

	// Check if project directory exists
	if _, err := os.Stat(projectDir); os.IsNotExist(err) {
		return ""
	}

	// Find session files (UUID format, not agent-* files)
	files, err := filepath.Glob(filepath.Join(projectDir, "*.jsonl"))
	if err != nil || len(files) == 0 {
		return ""
	}

	// UUID pattern for session files
	uuidPattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.jsonl$`)

	var mostRecent string
	var mostRecentTime time.Time

	for _, file := range files {
		base := filepath.Base(file)

		// Skip agent files (agent-*.jsonl)
		if strings.HasPrefix(base, "agent-") {
			continue
		}

		// Only consider UUID-named files
		if !uuidPattern.MatchString(base) {
			continue
		}

		sessionID := strings.TrimSuffix(base, ".jsonl")

		// Skip IDs owned by other agent-deck instances
		if excludeIDs[sessionID] {
			continue
		}

		info, err := os.Stat(file)
		if err != nil {
			continue
		}

		// Find the most recently modified file
		if info.ModTime().After(mostRecentTime) {
			mostRecentTime = info.ModTime()
			mostRecent = sessionID
		}
	}

	// Only return if modified within last 5 minutes (actively used)
	if mostRecent != "" && time.Since(mostRecentTime) < 5*time.Minute {
		return mostRecent
	}

	return ""
}

// getProjectSettingsPath returns the path to .claude/settings.local.json for a project
func getProjectSettingsPath(projectPath string) string {
	return filepath.Join(projectPath, ".claude", "settings.local.json")
}

// readProjectMCPSettings reads the project's MCP settings file
func readProjectMCPSettings(projectPath string) (*ProjectMCPSettings, error) {
	settingsPath := getProjectSettingsPath(projectPath)
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			// No settings file = default (all enabled)
			return &ProjectMCPSettings{}, nil
		}
		return nil, fmt.Errorf("failed to read settings: %w", err)
	}

	var settings ProjectMCPSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("failed to parse settings: %w", err)
	}

	return &settings, nil
}

// GetMCPMode determines the MCP configuration mode for a project
func GetMCPMode(projectPath string) MCPMode {
	settings, err := readProjectMCPSettings(projectPath)
	if err != nil {
		return MCPModeDefault
	}

	// Whitelist takes priority if set
	if len(settings.EnabledMcpjsonServers) > 0 {
		return MCPModeWhitelist
	}

	// Check for blacklist
	if len(settings.DisabledMcpjsonServers) > 0 {
		return MCPModeBlacklist
	}

	return MCPModeDefault
}

// GetLocalMCPState returns Local MCPs with their enabled state
func GetLocalMCPState(projectPath string) ([]MCPServer, error) {
	// Get all Local MCPs from .mcp.json
	mcpFile := filepath.Join(projectPath, ".mcp.json")
	data, err := os.ReadFile(mcpFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No .mcp.json = no Local MCPs
		}
		return nil, fmt.Errorf("failed to read .mcp.json: %w", err)
	}

	var mcpConfig projectMCPConfig
	if err := json.Unmarshal(data, &mcpConfig); err != nil {
		return nil, fmt.Errorf("failed to parse .mcp.json: %w", err)
	}

	if len(mcpConfig.MCPServers) == 0 {
		return nil, nil
	}

	// Get settings to determine enabled state
	settings, err := readProjectMCPSettings(projectPath)
	if err != nil {
		return nil, err
	}

	mode := GetMCPMode(projectPath)

	// Build result with enabled state
	var servers []MCPServer
	for name := range mcpConfig.MCPServers {
		enabled := isMCPEnabled(name, settings, mode)
		servers = append(servers, MCPServer{
			Name:    name,
			Source:  "local",
			Enabled: enabled,
		})
	}

	// Sort for consistent display
	sort.Slice(servers, func(i, j int) bool {
		return servers[i].Name < servers[j].Name
	})

	return servers, nil
}

// isMCPEnabled determines if an MCP is enabled based on settings and mode
func isMCPEnabled(name string, settings *ProjectMCPSettings, mode MCPMode) bool {
	switch mode {
	case MCPModeWhitelist:
		// Whitelist: enabled only if in enabledMcpjsonServers
		for _, enabled := range settings.EnabledMcpjsonServers {
			if enabled == name {
				return true
			}
		}
		return false

	case MCPModeBlacklist:
		// Blacklist: enabled unless in disabledMcpjsonServers
		for _, disabled := range settings.DisabledMcpjsonServers {
			if disabled == name {
				return false
			}
		}
		return true

	default:
		// Default: all enabled
		return true
	}
}

// PruneMCPCache removes cache entries older than maxAge to prevent unbounded growth.
// Called periodically from the TUI tick handler.
func PruneMCPCache(maxAge time.Duration) {
	mcpInfoCacheMu.Lock()
	defer mcpInfoCacheMu.Unlock()
	now := time.Now()
	for path, t := range mcpCacheTimes {
		if now.Sub(t) > maxAge {
			delete(mcpInfoCache, path)
			delete(mcpCacheTimes, path)
		}
	}
}

// ClearMCPCache invalidates the MCP cache for a project path and all parent directories
// This is important because getMCPInfoUncached walks up parent directories to find .mcp.json
func ClearMCPCache(projectPath string) {
	mcpInfoCacheMu.Lock()
	defer mcpInfoCacheMu.Unlock()

	// Clear the exact path
	delete(mcpInfoCache, projectPath)
	delete(mcpCacheTimes, projectPath)

	// Also clear all parent directories (MCP lookup walks up the tree)
	currentPath := projectPath
	for {
		parent := filepath.Dir(currentPath)
		if parent == currentPath || parent == "/" || parent == "." {
			break
		}
		delete(mcpInfoCache, parent)
		delete(mcpCacheTimes, parent)
		currentPath = parent
	}
}

// ToggleLocalMCP toggles a Local MCP on/off
// It respects the existing mode (whitelist vs blacklist) or initializes with blacklist
func ToggleLocalMCP(projectPath, mcpName string) error {
	// Read current settings (preserving other fields)
	settingsPath := getProjectSettingsPath(projectPath)
	var rawSettings map[string]interface{}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to read settings: %w", err)
		}
		// File doesn't exist, start fresh
		rawSettings = make(map[string]interface{})
	} else {
		if err := json.Unmarshal(data, &rawSettings); err != nil {
			return fmt.Errorf("failed to parse settings: %w", err)
		}
	}

	// Detect mode
	mode := GetMCPMode(projectPath)

	// Get current enabled state
	settings, _ := readProjectMCPSettings(projectPath)
	currentlyEnabled := isMCPEnabled(mcpName, settings, mode)

	// Toggle based on mode
	switch mode {
	case MCPModeWhitelist:
		// Modify enabledMcpjsonServers
		enabled := getStringSlice(rawSettings, "enabledMcpjsonServers")
		if currentlyEnabled {
			// Disable: remove from whitelist
			enabled = removeFromSlice(enabled, mcpName)
		} else {
			// Enable: add to whitelist
			enabled = appendIfMissing(enabled, mcpName)
		}
		rawSettings["enabledMcpjsonServers"] = enabled

	case MCPModeBlacklist:
		// Modify disabledMcpjsonServers
		disabled := getStringSlice(rawSettings, "disabledMcpjsonServers")
		if currentlyEnabled {
			// Disable: add to blacklist
			disabled = appendIfMissing(disabled, mcpName)
		} else {
			// Enable: remove from blacklist
			disabled = removeFromSlice(disabled, mcpName)
		}
		rawSettings["disabledMcpjsonServers"] = disabled

	default:
		// No mode set, initialize with blacklist
		if currentlyEnabled {
			// Disable: add to blacklist
			rawSettings["disabledMcpjsonServers"] = []string{mcpName}
		}
		// Enable does nothing (already enabled by default)
	}

	// Ensure .claude directory exists
	claudeDir := filepath.Join(projectPath, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		return fmt.Errorf("failed to create .claude directory: %w", err)
	}

	// Write atomically (temp file + rename)
	newData, err := json.MarshalIndent(rawSettings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	tmpPath := settingsPath + ".tmp"
	if err := os.WriteFile(tmpPath, newData, 0600); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	if err := os.Rename(tmpPath, settingsPath); err != nil {
		os.Remove(tmpPath) // Clean up on failure
		return fmt.Errorf("failed to rename settings file: %w", err)
	}

	// Clear cache so changes are reflected
	ClearMCPCache(projectPath)

	return nil
}

// getStringSlice extracts a string slice from a map
func getStringSlice(m map[string]interface{}, key string) []string {
	val, ok := m[key]
	if !ok {
		return nil
	}

	arr, ok := val.([]interface{})
	if !ok {
		return nil
	}

	result := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// removeFromSlice removes a string from a slice
func removeFromSlice(slice []string, item string) []string {
	result := make([]string, 0, len(slice))
	for _, s := range slice {
		if s != item {
			result = append(result, s)
		}
	}
	return result
}

// appendIfMissing adds a string to a slice if not already present
func appendIfMissing(slice []string, item string) []string {
	for _, s := range slice {
		if s == item {
			return slice
		}
	}
	return append(slice, item)
}
