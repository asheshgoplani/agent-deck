package session

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/BurntSushi/toml"
	"github.com/asheshgoplani/agent-deck/internal/platform"
)

// UserConfigFileName is the TOML config file for user preferences
const UserConfigFileName = "config.toml"

// UserConfig represents user-facing configuration in TOML format
type UserConfig struct {
	// DefaultTool is the pre-selected AI tool when creating new sessions
	// Valid values: "claude", "gemini", "opencode", "codex", or any custom tool name
	// If empty or invalid, defaults to "shell" (no pre-selection)
	DefaultTool string `toml:"default_tool"`

	// Theme sets the color scheme: "dark" (default) or "light"
	Theme string `toml:"theme"`

	// Tools defines custom AI tool configurations
	Tools map[string]ToolDef `toml:"tools"`

	// MCPs defines available MCP servers for the MCP Manager
	// These can be attached/detached per-project via the MCP Manager (M key)
	MCPs map[string]MCPDef `toml:"mcps"`

	// Claude defines Claude Code integration settings
	Claude ClaudeSettings `toml:"claude"`

	// Gemini defines Gemini CLI integration settings
	Gemini GeminiSettings `toml:"gemini"`

	// Worktree defines git worktree preferences
	Worktree WorktreeSettings `toml:"worktree"`

	// GlobalSearch defines global conversation search settings
	GlobalSearch GlobalSearchSettings `toml:"global_search"`

	// Logs defines session log management settings
	Logs LogSettings `toml:"logs"`

	// MCPPool defines HTTP MCP pool settings for shared MCP servers
	MCPPool MCPPoolSettings `toml:"mcp_pool"`

	// Updates defines auto-update settings
	Updates UpdateSettings `toml:"updates"`

	// Preview defines preview pane display settings
	Preview PreviewSettings `toml:"preview"`
}

// MCPPoolSettings defines HTTP MCP pool configuration
type MCPPoolSettings struct {
	// Enabled enables HTTP pool mode (default: false)
	Enabled bool `toml:"enabled"`

	// AutoStart starts pool when agent-deck launches (default: true)
	AutoStart bool `toml:"auto_start"`

	// PortStart is the first port in the pool range (default: 8001)
	PortStart int `toml:"port_start"`

	// PortEnd is the last port in the pool range (default: 8050)
	PortEnd int `toml:"port_end"`

	// StartOnDemand starts MCPs lazily on first attach (default: false)
	StartOnDemand bool `toml:"start_on_demand"`

	// ShutdownOnExit stops HTTP servers when agent-deck quits (default: true)
	ShutdownOnExit bool `toml:"shutdown_on_exit"`

	// PoolMCPs is the list of MCPs to run in pool mode
	// Empty = auto-detect common MCPs (memory, exa, firecrawl, etc.)
	PoolMCPs []string `toml:"pool_mcps"`

	// FallbackStdio uses stdio for MCPs without socket support (default: true)
	FallbackStdio bool `toml:"fallback_to_stdio"`

	// ShowStatus shows pool status in TUI (default: true)
	ShowStatus bool `toml:"show_pool_status"`

	// PoolAll pools all MCPs by default (default: false)
	PoolAll bool `toml:"pool_all"`

	// ExcludeMCPs excludes specific MCPs from pool when pool_all = true
	ExcludeMCPs []string `toml:"exclude_mcps"`

	// SocketWaitTimeout is seconds to wait for socket to become ready (default: 5)
	SocketWaitTimeout int `toml:"socket_wait_timeout"`
}

// LogSettings defines log file management configuration
type LogSettings struct {
	// MaxSizeMB is the maximum size in MB before a log file is truncated
	// When a log exceeds this size, it keeps only the last MaxLines lines
	// Default: 10 (10MB)
	MaxSizeMB int `toml:"max_size_mb"`

	// MaxLines is the number of lines to keep when truncating
	// Default: 10000
	MaxLines int `toml:"max_lines"`

	// RemoveOrphans removes log files for sessions that no longer exist
	// Default: true
	RemoveOrphans bool `toml:"remove_orphans"`
}

// UpdateSettings defines auto-update configuration
type UpdateSettings struct {
	// AutoUpdate automatically installs updates without prompting
	// Default: false
	AutoUpdate bool `toml:"auto_update"`

	// CheckEnabled enables automatic update checks on startup
	// Default: true
	CheckEnabled bool `toml:"check_enabled"`

	// CheckIntervalHours is how often to check for updates (in hours)
	// Default: 24
	CheckIntervalHours int `toml:"check_interval_hours"`

	// NotifyInCLI shows update notification in CLI commands (not just TUI)
	// Default: true
	NotifyInCLI bool `toml:"notify_in_cli"`
}

// PreviewSettings defines preview pane configuration
type PreviewSettings struct {
	// ShowOutput shows terminal output in preview pane (including launch animation)
	// Default: true (pointer to distinguish "not set" from "explicitly false")
	ShowOutput *bool `toml:"show_output"`

	// ShowAnalytics shows session analytics panel for Claude sessions
	// Default: true (pointer to distinguish "not set" from "explicitly false")
	ShowAnalytics *bool `toml:"show_analytics"`
}

// GetShowAnalytics returns whether to show analytics, defaulting to true
func (p *PreviewSettings) GetShowAnalytics() bool {
	if p.ShowAnalytics == nil {
		return true // Default: analytics ON
	}
	return *p.ShowAnalytics
}

// GetShowOutput returns whether to show terminal output, defaulting to true
func (p *PreviewSettings) GetShowOutput() bool {
	if p.ShowOutput == nil {
		return true // Default: output ON (shows launch animation)
	}
	return *p.ShowOutput
}

// GetShowOutput returns whether to show terminal output in preview
func (c *UserConfig) GetShowOutput() bool {
	return c.Preview.GetShowOutput()
}

// GetShowAnalytics returns whether to show analytics panel, defaulting to true
func (c *UserConfig) GetShowAnalytics() bool {
	return c.Preview.GetShowAnalytics()
}

// ClaudeSettings defines Claude Code configuration
type ClaudeSettings struct {
	// Command is the Claude CLI command or alias to use (e.g., "claude", "cdw", "cdp")
	// Default: "claude"
	// This allows using shell aliases that set CLAUDE_CONFIG_DIR automatically
	Command string `toml:"command"`

	// ConfigDir is the path to Claude's config directory
	// Default: ~/.claude (or CLAUDE_CONFIG_DIR env var)
	ConfigDir string `toml:"config_dir"`

	// DangerousMode enables --dangerously-skip-permissions flag for Claude sessions
	// Default: false
	DangerousMode bool `toml:"dangerous_mode"`
}

// GeminiSettings defines Gemini CLI configuration
type GeminiSettings struct {
	// YoloMode enables --yolo flag for Gemini sessions (auto-approve all actions)
	// Default: false
	YoloMode bool `toml:"yolo_mode"`
}

// WorktreeSettings contains git worktree preferences
type WorktreeSettings struct {
	// DefaultLocation: "sibling" (next to repo) or "subdirectory" (inside .worktrees/)
	DefaultLocation string `toml:"default_location"`
	// AutoCleanup: remove worktree when session is deleted
	AutoCleanup bool `toml:"auto_cleanup"`
}

// GlobalSearchSettings defines global conversation search configuration
type GlobalSearchSettings struct {
	// Enabled enables/disables global search feature (default: true when loaded via LoadUserConfig)
	Enabled bool `toml:"enabled"`

	// Tier controls search strategy: "auto", "instant", "balanced", "disabled"
	// auto: Auto-detect based on data size (recommended)
	// instant: Force full in-memory (fast, uses more RAM)
	// balanced: Force LRU cache mode (slower, capped RAM)
	// disabled: Disable global search entirely
	Tier string `toml:"tier"`

	// MemoryLimitMB caps memory usage for search index (default: 100)
	// Only applies to balanced tier
	MemoryLimitMB int `toml:"memory_limit_mb"`

	// RecentDays limits search to sessions from last N days (0 = all)
	// Reduces index size for users with long history (default: 90)
	RecentDays int `toml:"recent_days"`

	// IndexRateLimit limits files indexed per second during background indexing
	// Lower = less CPU impact (default: 20)
	IndexRateLimit int `toml:"index_rate_limit"`
}

// ToolDef defines a custom AI tool
type ToolDef struct {
	// Command is the shell command to run
	Command string `toml:"command"`

	// Icon is the emoji/symbol to display
	Icon string `toml:"icon"`

	// BusyPatterns are strings that indicate the tool is busy
	BusyPatterns []string `toml:"busy_patterns"`

	// PromptPatterns are strings that indicate the tool is waiting for input
	PromptPatterns []string `toml:"prompt_patterns"`

	// DetectPatterns are regex patterns to auto-detect this tool from terminal content
	DetectPatterns []string `toml:"detect_patterns"`

	// ResumeFlag is the CLI flag to resume a session (e.g., "--resume")
	ResumeFlag string `toml:"resume_flag"`

	// SessionIDEnv is the tmux environment variable name storing the session ID
	SessionIDEnv string `toml:"session_id_env"`

	// DangerousMode enables dangerous mode flag for this tool
	DangerousMode bool `toml:"dangerous_mode"`

	// DangerousFlag is the CLI flag for dangerous mode (e.g., "--dangerously-skip-permissions")
	DangerousFlag string `toml:"dangerous_flag"`

	// OutputFormatFlag is the CLI flag for JSON output format (e.g., "--output-format json")
	OutputFormatFlag string `toml:"output_format_flag"`

	// SessionIDJsonPath is the jq path to extract session ID from JSON output
	SessionIDJsonPath string `toml:"session_id_json_path"`
}

// MCPDef defines an MCP server configuration for the MCP Manager
type MCPDef struct {
	// Command is the executable to run (e.g., "npx", "docker", "node")
	// Required for stdio MCPs, optional for HTTP/SSE MCPs
	Command string `toml:"command"`

	// Args are command-line arguments
	Args []string `toml:"args"`

	// Env is optional environment variables
	Env map[string]string `toml:"env"`

	// Description is optional help text shown in the MCP Manager
	Description string `toml:"description"`

	// URL is the endpoint for HTTP/SSE MCPs (e.g., "http://localhost:8000/mcp")
	// If set, this MCP uses HTTP or SSE transport instead of stdio
	URL string `toml:"url"`

	// Transport specifies the MCP transport type: "stdio" (default), "http", or "sse"
	// Only needed when URL is set; defaults to "http" if URL is present
	Transport string `toml:"transport"`

	// Headers is optional HTTP headers for HTTP/SSE MCPs (e.g., for authentication)
	// Example: { Authorization = "Bearer token123" }
	Headers map[string]string `toml:"headers"`
}

// Default user config (empty maps)
var defaultUserConfig = UserConfig{
	Tools: make(map[string]ToolDef),
	MCPs:  make(map[string]MCPDef),
}

// Cache for user config (loaded once per session)
var (
	userConfigCache   *UserConfig
	userConfigCacheMu sync.RWMutex
)

// GetUserConfigPath returns the path to the user config file
func GetUserConfigPath() (string, error) {
	dir, err := GetAgentDeckDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, UserConfigFileName), nil
}

// LoadUserConfig loads the user configuration from TOML file
// Returns cached config after first load
func LoadUserConfig() (*UserConfig, error) {
	userConfigCacheMu.RLock()
	if userConfigCache != nil {
		defer userConfigCacheMu.RUnlock()
		return userConfigCache, nil
	}
	userConfigCacheMu.RUnlock()

	// Load config (only happens once)
	userConfigCacheMu.Lock()
	defer userConfigCacheMu.Unlock()

	// Double-check after acquiring write lock
	if userConfigCache != nil {
		return userConfigCache, nil
	}

	configPath, err := GetUserConfigPath()
	if err != nil {
		userConfigCache = &defaultUserConfig
		return userConfigCache, nil
	}

	// Check if config exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		// Return default config (no file exists yet)
		userConfigCache = &defaultUserConfig
		return userConfigCache, nil
	}

	var config UserConfig
	if _, err := toml.DecodeFile(configPath, &config); err != nil {
		// Return error so caller can display it to user
		// Still cache default to prevent repeated parse attempts
		userConfigCache = &defaultUserConfig
		return userConfigCache, fmt.Errorf("config.toml parse error: %w", err)
	}

	// Initialize maps if nil
	if config.Tools == nil {
		config.Tools = make(map[string]ToolDef)
	}
	if config.MCPs == nil {
		config.MCPs = make(map[string]MCPDef)
	}

	userConfigCache = &config
	return userConfigCache, nil
}

// ReloadUserConfig forces a reload of the user config
func ReloadUserConfig() (*UserConfig, error) {
	userConfigCacheMu.Lock()
	userConfigCache = nil
	userConfigCacheMu.Unlock()
	return LoadUserConfig()
}

// SaveUserConfig writes the config to config.toml using atomic write pattern
// This clears the cache so next LoadUserConfig() reads fresh values
func SaveUserConfig(config *UserConfig) error {
	configPath, err := GetUserConfigPath()
	if err != nil {
		return fmt.Errorf("failed to get config path: %w", err)
	}

	// Ensure directory exists
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Build config content in memory first
	var buf bytes.Buffer

	// Write header comment
	if _, err := buf.WriteString("# Agent Deck Configuration\n"); err != nil {
		return fmt.Errorf("failed to write header: %w", err)
	}
	if _, err := buf.WriteString("# Edit this file or use Settings (press S) in the TUI\n\n"); err != nil {
		return fmt.Errorf("failed to write header: %w", err)
	}

	// Encode to TOML
	encoder := toml.NewEncoder(&buf)
	if err := encoder.Encode(config); err != nil {
		return fmt.Errorf("failed to encode config: %w", err)
	}

	// ═══════════════════════════════════════════════════════════════════
	// ATOMIC WRITE PATTERN: Prevents data corruption on crash/power loss
	// 1. Write to temporary file with 0600 permissions
	// 2. fsync the temp file (ensures data reaches disk)
	// 3. Atomic rename temp to final
	// ═══════════════════════════════════════════════════════════════════

	tmpPath := configPath + ".tmp"

	// Step 1: Write to temporary file (0600 = owner read/write only for security)
	if err := os.WriteFile(tmpPath, buf.Bytes(), 0600); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	// Step 2: fsync the temp file to ensure data reaches disk before rename
	if err := syncConfigFile(tmpPath); err != nil {
		// Log but don't fail - atomic rename still provides some safety
		// Note: We don't have access to log package here, so we just continue
		_ = err
	}

	// Step 3: Atomic rename (this is atomic on POSIX systems)
	if err := os.Rename(tmpPath, configPath); err != nil {
		// Clean up temp file on failure
		os.Remove(tmpPath)
		return fmt.Errorf("failed to finalize config save: %w", err)
	}

	// Clear cache so next load picks up changes
	ClearUserConfigCache()

	return nil
}

// syncConfigFile calls fsync on a file to ensure data is written to disk
func syncConfigFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// ClearUserConfigCache clears the cached user config, allowing tests to reset state
// This does NOT reload - the next LoadUserConfig() call will read fresh from disk
func ClearUserConfigCache() {
	userConfigCacheMu.Lock()
	userConfigCache = nil
	userConfigCacheMu.Unlock()
}

// GetToolDef returns a tool definition from user config
// Returns nil if tool is not defined
func GetToolDef(toolName string) *ToolDef {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return nil
	}

	if def, ok := config.Tools[toolName]; ok {
		return &def
	}
	return nil
}

// GetToolIcon returns the icon for a tool (custom or built-in)
func GetToolIcon(toolName string) string {
	// Check custom tools first
	if def := GetToolDef(toolName); def != nil && def.Icon != "" {
		return def.Icon
	}

	// Built-in icons
	switch toolName {
	case "claude":
		return "🤖"
	case "gemini":
		return "✨"
	case "opencode":
		return "🌐"
	case "codex":
		return "💻"
	case "cursor":
		return "📝"
	case "shell":
		return "🐚"
	default:
		return "🐚"
	}
}

// GetToolBusyPatterns returns busy patterns for a tool (custom + built-in)
func GetToolBusyPatterns(toolName string) []string {
	var patterns []string

	// Add custom patterns first
	if def := GetToolDef(toolName); def != nil {
		patterns = append(patterns, def.BusyPatterns...)
	}

	// Built-in patterns are handled by the detector
	return patterns
}

// GetDefaultTool returns the user's preferred default tool for new sessions
// Returns empty string if not configured (defaults to shell)
func GetDefaultTool() string {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return ""
	}
	return config.DefaultTool
}

// GetTheme returns the current theme, defaulting to "dark"
func GetTheme() string {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return "dark"
	}
	if config.Theme == "" || (config.Theme != "dark" && config.Theme != "light") {
		return "dark"
	}
	return config.Theme
}

// GetLogSettings returns log management settings with defaults applied
func GetLogSettings() LogSettings {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return LogSettings{
			MaxSizeMB:     10,
			MaxLines:      10000,
			RemoveOrphans: true,
		}
	}

	settings := config.Logs

	// Apply defaults for unset values
	if settings.MaxSizeMB <= 0 {
		settings.MaxSizeMB = 10
	}
	if settings.MaxLines <= 0 {
		settings.MaxLines = 10000
	}
	// RemoveOrphans defaults to true (Go zero value is false, so we check if config was loaded)
	// If the config file doesn't have this key, we want it to be true by default
	// We detect this by checking if the entire Logs section is empty
	if config.Logs.MaxSizeMB == 0 && config.Logs.MaxLines == 0 {
		settings.RemoveOrphans = true
	}

	return settings
}

// GetWorktreeSettings returns worktree settings with defaults applied
func GetWorktreeSettings() WorktreeSettings {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return WorktreeSettings{
			DefaultLocation: "sibling",
			AutoCleanup:     true,
		}
	}

	settings := config.Worktree

	// Apply defaults for unset values
	if settings.DefaultLocation == "" {
		settings.DefaultLocation = "sibling"
	}
	// AutoCleanup defaults to true (Go zero value is false)
	// We detect if section was not present by checking if DefaultLocation is empty
	if config.Worktree.DefaultLocation == "" {
		settings.AutoCleanup = true
	}

	return settings
}

// GetUpdateSettings returns update settings with defaults applied
func GetUpdateSettings() UpdateSettings {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return UpdateSettings{
			AutoUpdate:         false,
			CheckEnabled:       true,
			CheckIntervalHours: 24,
			NotifyInCLI:        true,
		}
	}

	settings := config.Updates

	// Apply defaults for unset values
	// CheckEnabled defaults to true (need to detect if section exists)
	if config.Updates.CheckIntervalHours == 0 {
		settings.CheckEnabled = true
		settings.CheckIntervalHours = 24
		settings.NotifyInCLI = true
	}
	if settings.CheckIntervalHours <= 0 {
		settings.CheckIntervalHours = 24
	}

	return settings
}

// GetPreviewSettings returns preview settings with defaults applied
func GetPreviewSettings() PreviewSettings {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return PreviewSettings{
			ShowOutput:    nil, // nil means "default to true"
			ShowAnalytics: nil, // nil means "default to true"
		}
	}

	return config.Preview
}

// getMCPPoolConfigSection returns the MCP pool config section based on platform
// On unsupported platforms (WSL1, Windows), it's commented out with explanation
func getMCPPoolConfigSection() string {
	header := `
# ============================================================================
# MCP Socket Pool (Advanced)
# ============================================================================
# The MCP pool shares MCP processes across multiple Claude sessions via Unix
# domain sockets. This reduces memory usage when running many sessions.
#
# PLATFORM SUPPORT:
#   macOS/Linux: Full support
#   WSL2: Full support
#   WSL1: NOT SUPPORTED (Unix sockets unreliable)
#   Windows: NOT SUPPORTED
#
# When pooling is disabled or unsupported, MCPs use stdio mode (default).
# Both modes work identically - pooling is just a memory optimization.

`
	if platform.SupportsUnixSockets() {
		// Platform supports pooling - show enabled example
		return header + `# Uncomment to enable MCP socket pooling:
# [mcp_pool]
# enabled = true
# pool_all = true           # Pool all MCPs defined above
# fallback_to_stdio = true  # Fall back to stdio if socket fails
# exclude_mcps = []         # MCPs to exclude from pooling
`
	}

	// Platform doesn't support pooling - explain why it's disabled
	p := platform.Detect()
	reason := "Unix sockets not supported"
	tip := ""

	switch p {
	case platform.PlatformWSL1:
		reason = "WSL1 detected - Unix sockets unreliable"
		tip = "\n# TIP: Upgrade to WSL2 for socket pooling support:\n#      wsl --set-version <distro> 2\n"
	case platform.PlatformWindows:
		reason = "Windows detected - Unix sockets not available"
	}

	return header + fmt.Sprintf(`# MCP pool is DISABLED on this platform: %s
# MCPs will use stdio mode (works fine, just uses more memory with many sessions).
%s
# [mcp_pool]
# enabled = false  # Cannot be enabled on this platform
`, reason, tip)
}

// CreateExampleConfig creates an example config file if none exists
func CreateExampleConfig() error {
	configPath, err := GetUserConfigPath()
	if err != nil {
		return err
	}

	// Don't overwrite existing config
	if _, err := os.Stat(configPath); err == nil {
		return nil
	}

	exampleConfig := `# Agent Deck User Configuration
# This file is loaded on startup. Edit to customize tools and MCPs.

# Default AI tool for new sessions
# When creating a new session (pressing 'n'), this tool will be pre-selected
# Valid values: "claude", "gemini", "opencode", "codex", or any custom tool name
# Leave commented out or empty to default to shell (no pre-selection)
# default_tool = "claude"

# Claude Code integration
# Set this if you use a custom Claude profile (e.g., dual account setup)
# Default: ~/.claude (or CLAUDE_CONFIG_DIR env var takes priority)
# [claude]
# config_dir = "~/.claude-work"

# Log file management
# Agent-deck logs session output to ~/.agent-deck/logs/ for status detection
# These settings control automatic log maintenance to prevent disk bloat
[logs]
# Maximum log file size in MB before truncation (default: 10)
max_size_mb = 10
# Number of lines to keep when truncating (default: 10000)
max_lines = 10000
# Remove log files for sessions that no longer exist (default: true)
remove_orphans = true

# Update settings
# Controls automatic update checking and installation
[updates]
# Automatically install updates without prompting (default: false)
# auto_update = true
# Enable update checks on startup (default: true)
check_enabled = true
# How often to check for updates in hours (default: 24)
check_interval_hours = 24
# Show update notification in CLI commands, not just TUI (default: true)
notify_in_cli = true

# ============================================================================
# MCP Server Definitions
# ============================================================================
# Define available MCP servers here. These can be attached/detached per-project
# using the MCP Manager (press 'M' on a Claude session).
#
# Supports two transport types:
#
# STDIO MCPs (local command-line tools):
#   command     - The executable to run (e.g., "npx", "docker", "node")
#   args        - Command-line arguments (array)
#   env         - Environment variables (optional)
#   description - Help text shown in the MCP Manager (optional)
#
# HTTP/SSE MCPs (remote servers):
#   url         - The endpoint URL (http:// or https://)
#   transport   - "http" or "sse" (defaults to "http" if url is set)
#   description - Help text shown in the MCP Manager (optional)

# ---------- STDIO Examples ----------

# Example: Exa Search MCP
# [mcps.exa]
# command = "npx"
# args = ["-y", "@anthropics/exa-mcp"]
# description = "Web search via Exa AI"

# Example: Filesystem MCP with restricted paths
# [mcps.filesystem]
# command = "npx"
# args = ["-y", "@modelcontextprotocol/server-filesystem", "/Users/you/projects"]
# description = "Read/write local files"

# Example: GitHub MCP with token
# [mcps.github]
# command = "npx"
# args = ["-y", "@modelcontextprotocol/server-github"]
# env = { GITHUB_TOKEN = "ghp_your_token_here" }
# description = "GitHub repository operations"

# Example: Sequential Thinking MCP
# [mcps.thinking]
# command = "npx"
# args = ["-y", "@modelcontextprotocol/server-sequential-thinking"]
# description = "Step-by-step reasoning for complex problems"

# ---------- HTTP/SSE Examples ----------

# Example: HTTP MCP server (local or remote)
# [mcps.my-http-server]
# url = "http://localhost:8000/mcp"
# transport = "http"
# description = "My custom HTTP MCP server"

# Example: HTTP MCP with authentication headers
# [mcps.authenticated-api]
# url = "https://api.example.com/mcp"
# transport = "http"
# headers = { Authorization = "Bearer your-token-here", "X-API-Key" = "your-api-key" }
# description = "HTTP MCP with auth headers"

# Example: SSE MCP server
# [mcps.remote-sse]
# url = "https://api.example.com/mcp/sse"
# transport = "sse"
# description = "Remote SSE-based MCP"

# ============================================================================
# Custom Tool Definitions
# ============================================================================
# Each tool can have:
#   command      - The shell command to run
#   icon         - Emoji/symbol shown in the UI
#   busy_patterns - Strings that indicate the tool is processing

# Example: Add a custom AI tool
# [tools.my-ai]
# command = "my-ai-assistant"
# icon = "🧠"
# busy_patterns = ["thinking...", "processing..."]

# Example: Add GitHub Copilot CLI
# [tools.copilot]
# command = "gh copilot"
# icon = "🤖"
# busy_patterns = ["Generating..."]
`

	// Add platform-aware MCP pool section
	exampleConfig += getMCPPoolConfigSection()

	// Ensure directory exists
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	return os.WriteFile(configPath, []byte(exampleConfig), 0600)
}

// GetAvailableMCPs returns MCPs from config.toml as a map
// This replaces the old catalog-based approach with explicit user configuration
func GetAvailableMCPs() map[string]MCPDef {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return make(map[string]MCPDef)
	}
	return config.MCPs
}

// GetAvailableMCPNames returns sorted list of MCP names from config.toml
func GetAvailableMCPNames() []string {
	mcps := GetAvailableMCPs()
	names := make([]string, 0, len(mcps))
	for name := range mcps {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// GetMCPDef returns a specific MCP definition by name
// Returns nil if not found
func GetMCPDef(name string) *MCPDef {
	mcps := GetAvailableMCPs()
	if def, ok := mcps[name]; ok {
		return &def
	}
	return nil
}
