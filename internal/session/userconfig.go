package session

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"

	dark "github.com/thiagokokada/dark-mode-go"

	"github.com/asheshgoplani/agent-deck/internal/platform"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// UserConfigFileName is the TOML config file for user preferences
const UserConfigFileName = "config.toml"

// UserConfig represents user-facing configuration in TOML format
type UserConfig struct {
	// DefaultTool is the pre-selected AI tool when creating new sessions
	// Valid values: "claude", "gemini", "opencode", "codex", or any custom tool name
	// If empty or invalid, defaults to "shell" (no pre-selection)
	DefaultTool string `toml:"default_tool"`

	// Theme sets the color scheme: "dark" (default), "light", or "system"
	Theme string `toml:"theme"`

	// Tools defines custom AI tool configurations
	Tools map[string]ToolDef `toml:"tools"`

	// MCPDefaultScope sets the default scope for MCP operations
	// Valid values: "local" (default), "global", "user"
	MCPDefaultScope string `toml:"mcp_default_scope"`

	// MCPs defines available MCP servers for the MCP Manager
	// These can be attached/detached per-project via the MCP Manager (M key)
	MCPs map[string]MCPDef `toml:"mcps"`

	// Claude defines Claude Code integration settings
	Claude ClaudeSettings `toml:"claude"`

	// Gemini defines Gemini CLI integration settings
	Gemini GeminiSettings `toml:"gemini"`

	// OpenCode defines OpenCode CLI integration settings
	OpenCode OpenCodeSettings `toml:"opencode"`

	// Codex defines Codex CLI integration settings
	Codex CodexSettings `toml:"codex"`

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

	// Experiments defines experiment folder settings for 'try' command
	Experiments ExperimentsSettings `toml:"experiments"`

	// Notifications defines waiting session notification bar settings
	Notifications NotificationsConfig `toml:"notifications"`

	// Instances defines multiple instance behavior settings
	Instances InstanceSettings `toml:"instances"`

	// Shell defines global shell environment settings for sessions
	Shell ShellSettings `toml:"shell"`

	// Maintenance defines automatic maintenance worker settings
	Maintenance MaintenanceSettings `toml:"maintenance"`

	// Status defines session status detection settings
	Status StatusSettings `toml:"status"`

	// Conductor defines conductor (meta-agent orchestration) settings
	Conductor ConductorSettings `toml:"conductor"`

	// Tmux defines tmux option overrides applied to every session
	Tmux TmuxSettings `toml:"tmux"`
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

	// DebugLevel sets the minimum log level: "debug", "info", "warn", "error"
	// Default: "info"
	DebugLevel string `toml:"debug_level"`

	// DebugFormat sets the log format: "json" (default) or "text"
	DebugFormat string `toml:"debug_format"`

	// DebugMaxMB is the max size in MB for debug.log before rotation
	// Default: 10
	DebugMaxMB int `toml:"debug_max_mb"`

	// DebugBackups is the number of rotated debug.log files to keep
	// Default: 5
	DebugBackups int `toml:"debug_backups"`

	// DebugRetentionDays is the number of days to keep rotated debug logs
	// Default: 10
	DebugRetentionDays int `toml:"debug_retention_days"`

	// DebugCompress enables gzip compression for rotated debug logs
	// Default: true
	DebugCompress bool `toml:"debug_compress"`

	// RingBufferMB is the in-memory ring buffer size in MB for crash dumps
	// Default: 10
	RingBufferMB int `toml:"ring_buffer_mb"`

	// PprofEnabled starts a pprof server on localhost:6060 when debug mode is active
	// Default: false
	PprofEnabled bool `toml:"pprof_enabled"`

	// AggregateIntervalS is the event aggregation flush interval in seconds
	// Default: 30
	AggregateIntervalS int `toml:"aggregate_interval_secs"`
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
	// Default: false (pointer to distinguish "not set" from "explicitly false")
	ShowAnalytics *bool `toml:"show_analytics"`

	// Analytics configures which sections to show in the analytics panel
	Analytics AnalyticsDisplaySettings `toml:"analytics"`
}

// AnalyticsDisplaySettings configures which analytics sections to display
// All settings use pointers to distinguish "not set" from "explicitly false"
type AnalyticsDisplaySettings struct {
	// ShowContextBar shows the context window usage bar (default: true)
	ShowContextBar *bool `toml:"show_context_bar"`

	// ShowTokens shows the token breakdown (In/Out/Cache/Total) (default: false)
	ShowTokens *bool `toml:"show_tokens"`

	// ShowSessionInfo shows duration, turns, start time (default: false)
	ShowSessionInfo *bool `toml:"show_session_info"`

	// ShowTools shows the top tool calls (default: true)
	ShowTools *bool `toml:"show_tools"`

	// ShowCost shows the estimated cost (default: false)
	ShowCost *bool `toml:"show_cost"`
}

// ExperimentsSettings defines experiment folder configuration
type ExperimentsSettings struct {
	// Directory is the base directory for experiments
	// Default: ~/src/tries
	Directory string `toml:"directory"`

	// DatePrefix adds YYYY-MM-DD- prefix to new experiment folders
	// Default: true
	DatePrefix bool `toml:"date_prefix"`

	// DefaultTool is the AI tool to use for experiment sessions
	// Default: "claude"
	DefaultTool string `toml:"default_tool"`
}

// NotificationsConfig configures the waiting session notification bar
type NotificationsConfig struct {
	// Enabled shows notification bar in tmux status (default: true)
	Enabled bool `toml:"enabled"`

	// MaxShown is the maximum number of sessions shown in the bar (default: 6)
	MaxShown int `toml:"max_shown"`
}

// InstanceSettings configures multiple agent-deck instance behavior
type InstanceSettings struct {
	// AllowMultiple allows running multiple agent-deck TUI instances for the same profile
	// When true (default), multiple instances can run, but only the first (primary) manages the notification bar
	// When false, only one instance can run per profile
	AllowMultiple *bool `toml:"allow_multiple"`
}

// GetAllowMultiple returns whether multiple instances are allowed, defaulting to true
func (i *InstanceSettings) GetAllowMultiple() bool {
	if i.AllowMultiple == nil {
		return true // Default: allow multiple instances (better UX for multi-pane workflows)
	}
	return *i.AllowMultiple
}

// ShellSettings defines shell environment configuration for sessions
type ShellSettings struct {
	// EnvFiles is a list of .env files to source for ALL sessions
	// Paths can be absolute, ~ for home, or relative to session working directory
	// Files are sourced in order; later files override earlier ones
	EnvFiles []string `toml:"env_files"`

	// InitScript is an optional shell script or command to run before each session
	// Useful for direnv, nvm, pyenv, etc.
	// Can be a file path (e.g., "~/.agent-deck/init.sh") or inline command
	// (e.g., 'eval "$(direnv hook bash)"')
	InitScript string `toml:"init_script"`

	// IgnoreMissingEnvFiles silently ignores missing .env files (default: true)
	// When false, sessions will error if an env_file doesn't exist
	IgnoreMissingEnvFiles *bool `toml:"ignore_missing_env_files"`
}

// GetIgnoreMissingEnvFiles returns whether to ignore missing env files, defaulting to true
func (s *ShellSettings) GetIgnoreMissingEnvFiles() bool {
	if s.IgnoreMissingEnvFiles == nil {
		return true // Default: ignore missing files (fail-safe)
	}
	return *s.IgnoreMissingEnvFiles
}

// GetShowAnalytics returns whether to show analytics, defaulting to false
func (p *PreviewSettings) GetShowAnalytics() bool {
	if p.ShowAnalytics == nil {
		return false // Default: analytics OFF (opt-in)
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

// GetAnalyticsSettings returns the analytics display settings with defaults applied
func (p *PreviewSettings) GetAnalyticsSettings() AnalyticsDisplaySettings {
	return p.Analytics
}

// GetShowContextBar returns whether to show context bar, defaulting to true
func (a *AnalyticsDisplaySettings) GetShowContextBar() bool {
	if a.ShowContextBar == nil {
		return true // Default: ON - useful visual indicator
	}
	return *a.ShowContextBar
}

// GetShowTokens returns whether to show token breakdown, defaulting to false
func (a *AnalyticsDisplaySettings) GetShowTokens() bool {
	if a.ShowTokens == nil {
		return false // Default: OFF - can be noisy
	}
	return *a.ShowTokens
}

// GetShowSessionInfo returns whether to show session info, defaulting to false
func (a *AnalyticsDisplaySettings) GetShowSessionInfo() bool {
	if a.ShowSessionInfo == nil {
		return false // Default: OFF - less useful info
	}
	return *a.ShowSessionInfo
}

// GetShowTools returns whether to show tool calls, defaulting to false
func (a *AnalyticsDisplaySettings) GetShowTools() bool {
	if a.ShowTools == nil {
		return false // Default: OFF - keeps display minimal
	}
	return *a.ShowTools
}

// GetShowCost returns whether to show cost estimate, defaulting to false
func (a *AnalyticsDisplaySettings) GetShowCost() bool {
	if a.ShowCost == nil {
		return false // Default: OFF - can be noisy
	}
	return *a.ShowCost
}

// GetShowOutput returns whether to show terminal output in preview
func (c *UserConfig) GetShowOutput() bool {
	return c.Preview.GetShowOutput()
}

// GetShowAnalytics returns whether to show analytics panel, defaulting to false
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
	// Default: true (nil = use default true, explicitly set false to disable)
	// Power users typically want this enabled for faster iteration
	DangerousMode *bool `toml:"dangerous_mode"`

	// AllowDangerousMode enables --allow-dangerously-skip-permissions flag
	// This unlocks bypass as an option without activating it by default.
	// Ignored when dangerous_mode is true (the stronger flag takes precedence).
	// Default: false
	AllowDangerousMode bool `toml:"allow_dangerous_mode"`

	// EnvFile is a .env file specific to Claude sessions
	// Sourced AFTER global [shell].env_files
	// Path can be absolute, ~ for home, or relative to session working directory
	EnvFile string `toml:"env_file"`
}

// GetDangerousMode returns whether dangerous mode is enabled, defaulting to true
// Power users (the primary audience) typically want this enabled for faster iteration
func (c *ClaudeSettings) GetDangerousMode() bool {
	if c.DangerousMode == nil {
		return true
	}
	return *c.DangerousMode
}

// GeminiSettings defines Gemini CLI configuration
type GeminiSettings struct {
	// YoloMode enables --yolo flag for Gemini sessions (auto-approve all actions)
	// Default: false
	YoloMode bool `toml:"yolo_mode"`

	// DefaultModel is the model to use for new Gemini sessions (e.g., "gemini-2.5-flash")
	// If empty, Gemini CLI uses its own default
	DefaultModel string `toml:"default_model"`

	// EnvFile is a .env file specific to Gemini sessions
	// Sourced AFTER global [shell].env_files
	EnvFile string `toml:"env_file"`
}

// OpenCodeSettings defines OpenCode CLI configuration
type OpenCodeSettings struct {
	// DefaultModel is the model to use for new OpenCode sessions
	// Format: "provider/model" (e.g., "anthropic/claude-sonnet-4-5-20250929")
	// If empty, OpenCode uses its own default
	DefaultModel string `toml:"default_model"`

	// DefaultAgent is the agent to use for new OpenCode sessions
	// If empty, OpenCode uses its own default
	DefaultAgent string `toml:"default_agent"`

	// EnvFile is a .env file specific to OpenCode sessions
	// Sourced AFTER global [shell].env_files
	EnvFile string `toml:"env_file"`
}

// CodexSettings defines Codex CLI configuration
type CodexSettings struct {
	// YoloMode enables --yolo flag for Codex sessions (bypass approvals and sandbox)
	// Default: false
	YoloMode bool `toml:"yolo_mode"`
}

// WorktreeSettings contains git worktree preferences.
type WorktreeSettings struct {
	// AutoCleanup: remove worktree when session is deleted
	AutoCleanup bool `toml:"auto_cleanup"`

	// DefaultLocation: "sibling" (next to repo), "subdirectory" (inside .worktrees/),
	// or a custom path (e.g., "~/worktrees") creating <path>/<repo_name>/<branch>
	DefaultLocation string `toml:"default_location"`

	// PathTemplate: custom path template for worktree location.
	// Variables: {repo-name}, {repo-root}, {branch}, {session-id}
	// Unknown variables like {foo} are left as-is in the path.
	// If set, overrides DefaultLocation.
	PathTemplate *string `toml:"path_template"`
}

// Template returns the path template if set, or empty string if nil.
func (w *WorktreeSettings) Template() string {
	if w.PathTemplate == nil {
		return ""
	}
	return *w.PathTemplate
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

	// Wrapper is an optional command that wraps the tool command.
	// Use {command} placeholder to include the tool command, or omit it to replace the command.
	// Example: wrapper = "nvim +'terminal {command}' +'startinsert'"
	Wrapper string `toml:"wrapper"`

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

	// EnvFile is a .env file specific to this tool
	// Sourced AFTER global [shell].env_files
	EnvFile string `toml:"env_file"`

	// Env is inline environment variables for this tool
	// These are exported AFTER env_file (highest priority)
	// Example: env = { ANTHROPIC_BASE_URL = "https://...", API_KEY = "token" }
	Env map[string]string `toml:"env"`

	// Pattern override fields (extend built-in defaults for claude/gemini/opencode/codex)
	// Patterns prefixed with "re:" are compiled as regex; everything else uses strings.Contains.

	// BusyPatternsExtra appends additional busy patterns to the built-in defaults
	BusyPatternsExtra []string `toml:"busy_patterns_extra"`

	// PromptPatternsExtra appends additional prompt patterns to the built-in defaults
	PromptPatternsExtra []string `toml:"prompt_patterns_extra"`

	// SpinnerChars replaces the default spinner characters entirely (use with caution)
	SpinnerChars []string `toml:"spinner_chars"`

	// SpinnerCharsExtra appends additional spinner characters to the built-in defaults
	SpinnerCharsExtra []string `toml:"spinner_chars_extra"`
}

// HTTPServerConfig defines how to auto-start an HTTP MCP server
type HTTPServerConfig struct {
	// Command is the executable to run (e.g., "uvx", "python", "node")
	Command string `toml:"command"`

	// Args are command-line arguments for the server
	Args []string `toml:"args"`

	// Env is environment variables for the server process
	Env map[string]string `toml:"env"`

	// StartupTimeout is milliseconds to wait for server to become ready (default: 5000)
	StartupTimeout int `toml:"startup_timeout"`

	// HealthCheck is an optional health endpoint URL to poll (e.g., "http://localhost:30000/health")
	// If not set, the main URL is used for health checking
	HealthCheck string `toml:"health_check"`
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

	// Server defines how to auto-start an HTTP MCP server process
	// When set, agent-deck will start the server before connecting via HTTP
	// This is optional - you can also connect to externally managed servers
	Server *HTTPServerConfig `toml:"server"`
}

// GetStartupTimeout returns the startup timeout in milliseconds, defaulting to 5000ms
func (c *HTTPServerConfig) GetStartupTimeout() int {
	if c.StartupTimeout <= 0 {
		return 5000 // Default: 5 seconds
	}
	return c.StartupTimeout
}

// IsHTTP returns true if this MCP uses HTTP or SSE transport
func (m *MCPDef) IsHTTP() bool {
	return m.URL != ""
}

// GetTransport returns the transport type, defaulting to "http" if URL is set
func (m *MCPDef) GetTransport() string {
	if m.URL == "" {
		return "stdio"
	}
	if m.Transport == "" {
		return "http"
	}
	return m.Transport
}

// HasAutoStartServer returns true if this HTTP MCP has server auto-start configured
func (m *MCPDef) HasAutoStartServer() bool {
	return m.IsHTTP() && m.Server != nil && m.Server.Command != ""
}

// TmuxSettings allows users to override tmux options applied to every session.
// Options are applied AFTER agent-deck's defaults, so they take precedence.
//
// Example config.toml:
//
//	[tmux]
//	options = { "allow-passthrough" = "all", "history-limit" = "50000" }
type TmuxSettings struct {
	// Options is a map of tmux option names to values.
	// These are passed to `tmux set-option -t <session>` after defaults.
	Options map[string]string `toml:"options"`
}

type StatusSettings struct {
	// Reserved for future status detection settings.
	// Control mode pipes are always enabled (no longer configurable).
}

// MaintenanceSettings controls the automatic maintenance worker
type MaintenanceSettings struct {
	// Enabled enables the maintenance worker (default: false)
	// Prunes Gemini logs, cleans old backups, archives bloated sessions
	Enabled bool `toml:"enabled"`
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
	if err := os.MkdirAll(dir, 0o700); err != nil {
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
	if err := os.WriteFile(tmpPath, buf.Bytes(), 0o600); err != nil {
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

// GetCustomToolNames returns sorted custom tool names from config.toml,
// excluding names that shadow built-in tools (claude, gemini, opencode, codex, shell, cursor, aider).
// Returns nil if no custom tools are configured.
func GetCustomToolNames() []string {
	config, err := LoadUserConfig()
	if err != nil || config == nil || len(config.Tools) == 0 {
		return nil
	}

	builtins := map[string]bool{
		"claude": true, "gemini": true, "opencode": true,
		"codex": true, "shell": true, "cursor": true, "aider": true,
	}

	var names []string
	for name := range config.Tools {
		if !builtins[name] {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	return names
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

// MergeToolPatterns returns merged RawPatterns for a tool, combining built-in
// defaults with any user overrides/extras from config.toml.
// Works for ALL tools: built-in (claude, gemini, etc.) and custom.
// Returns nil only if there are no defaults AND no config entry.
func MergeToolPatterns(toolName string) *tmux.RawPatterns {
	defaults := tmux.DefaultRawPatterns(toolName)
	toolDef := GetToolDef(toolName)

	// No defaults and no config entry: nothing to do
	if defaults == nil && toolDef == nil {
		return nil
	}

	// Build overrides from ToolDef's replace fields (BusyPatterns, PromptPatterns, SpinnerChars)
	var overrides *tmux.RawPatterns
	if toolDef != nil && (toolDef.BusyPatterns != nil || toolDef.PromptPatterns != nil || toolDef.SpinnerChars != nil) {
		overrides = &tmux.RawPatterns{
			BusyPatterns:   toolDef.BusyPatterns,
			PromptPatterns: toolDef.PromptPatterns,
			SpinnerChars:   toolDef.SpinnerChars,
		}
	}

	// Build extras from ToolDef's *Extra fields
	var extras *tmux.RawPatterns
	if toolDef != nil &&
		(len(toolDef.BusyPatternsExtra) > 0 || len(toolDef.PromptPatternsExtra) > 0 || len(toolDef.SpinnerCharsExtra) > 0) {
		extras = &tmux.RawPatterns{
			BusyPatterns:   toolDef.BusyPatternsExtra,
			PromptPatterns: toolDef.PromptPatternsExtra,
			SpinnerChars:   toolDef.SpinnerCharsExtra,
		}
	}

	return tmux.MergeRawPatterns(defaults, overrides, extras)
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
	switch config.Theme {
	case "dark", "light", "system":
		return config.Theme
	default:
		return "dark"
	}
}

// ResolveTheme resolves the configured theme to "dark" or "light".
// If theme is "system", detects the OS dark mode setting.
// Falls back to "dark" on detection failure.
func ResolveTheme() string {
	theme := GetTheme()
	if theme != "system" {
		return theme
	}
	isDark, err := dark.IsDarkMode()
	if err != nil {
		return "dark"
	}
	if isDark {
		return "dark"
	}
	return "light"
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
			DefaultLocation: "subdirectory",
			AutoCleanup:     true,
		}
	}

	settings := config.Worktree

	if settings.DefaultLocation == "" {
		settings.DefaultLocation = "subdirectory"
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

// GetExperimentsSettings returns experiments settings with defaults applied
func GetExperimentsSettings() ExperimentsSettings {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		homeDir, _ := os.UserHomeDir()
		return ExperimentsSettings{
			Directory:   filepath.Join(homeDir, "src", "tries"),
			DatePrefix:  true,
			DefaultTool: "claude",
		}
	}

	settings := config.Experiments

	// Apply defaults for unset values
	if settings.Directory == "" {
		homeDir, _ := os.UserHomeDir()
		settings.Directory = filepath.Join(homeDir, "src", "tries")
	} else {
		// Expand ~ in path
		if strings.HasPrefix(settings.Directory, "~/") {
			homeDir, _ := os.UserHomeDir()
			settings.Directory = filepath.Join(homeDir, settings.Directory[2:])
		}
	}

	// DatePrefix defaults to true (Go zero value is false, need explicit check)
	// If directory is default, assume DatePrefix should be true
	if config.Experiments.Directory == "" {
		settings.DatePrefix = true
	}

	if settings.DefaultTool == "" {
		settings.DefaultTool = "claude"
	}

	return settings
}

// GetNotificationsSettings returns notification bar settings with defaults applied
func GetNotificationsSettings() NotificationsConfig {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return NotificationsConfig{
			Enabled:  true,
			MaxShown: 6,
		}
	}

	settings := config.Notifications

	// Apply defaults for unset values
	// Enabled defaults to true for better UX (users expect to see waiting sessions)
	// Users who have a config file but no [notifications] section get enabled=true
	if !settings.Enabled && settings.MaxShown == 0 {
		// Section not explicitly configured, apply default
		settings.Enabled = true
	}
	if settings.MaxShown <= 0 {
		settings.MaxShown = 6
	}

	return settings
}

// GetMaintenanceSettings returns maintenance settings from config
func GetMaintenanceSettings() MaintenanceSettings {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return MaintenanceSettings{Enabled: false}
	}
	return config.Maintenance
}

// GetStatusSettings returns status detection settings with defaults applied.
func GetStatusSettings() StatusSettings {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return StatusSettings{}
	}
	return config.Status
}

// GetTmuxSettings returns tmux option overrides from config
func GetTmuxSettings() TmuxSettings {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return TmuxSettings{}
	}
	return config.Tmux
}

// GetInstanceSettings returns instance behavior settings
func GetInstanceSettings() InstanceSettings {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return InstanceSettings{} // Defaults applied via GetAllowMultiple()
	}
	return config.Instances
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
# [claude]
# Custom config directory (for dual account setups)
# Default: ~/.claude (or CLAUDE_CONFIG_DIR env var takes priority)
# config_dir = "~/.claude-work"
# Enable --dangerously-skip-permissions by default (default: false)
# dangerous_mode = true

# Gemini CLI integration
# [gemini]
# Enable --yolo (auto-approve all actions) by default (default: false)
# yolo_mode = true

# OpenCode CLI integration
# [opencode]
# Default model for new sessions (format: "provider/model")
# default_model = "anthropic/claude-sonnet-4-5-20250929"
# Default agent for new sessions
# default_agent = ""

# Codex CLI integration
# [codex]
# Enable --yolo (bypass approvals and sandbox) by default (default: false)
# yolo_mode = true

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

# Experiments (for 'agent-deck try' command)
# Quick experiment folder management with auto-dated directories
[experiments]
# Base directory for experiments (default: ~/src/tries)
directory = "~/src/tries"
# Add YYYY-MM-DD- prefix to new experiment folders (default: true)
date_prefix = true
# Default AI tool for experiment sessions (default: "claude")
default_tool = "claude"

# Git worktree settings
# Worktrees allow creating isolated working directories for branches
[worktree]
# Where to create worktrees: "sibling" (next to repo) or "subdirectory" (inside repo)
default_location = "sibling"
# Automatically remove worktree when session is deleted
auto_cleanup = true
# Custom path template (overrides default_location if set)
# Variables: {repo-name}, {repo-root}, {branch}, {session-id}
# path_template = "../worktrees/{repo-name}/{branch}"

# Default scope for MCP operations: "local", "global", or "user"
# "local" writes to .mcp.json (project-only, default)
# "global" writes to Claude profile config (profile-wide)
# "user" writes to ~/.claude.json (all profiles)
# mcp_default_scope = "local"

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

# ---------- HTTP MCP with Auto-Start Server ----------
# For MCPs that need a local server process (e.g., piekstra/slack-mcp-server),
# add a [mcps.NAME.server] block to have agent-deck auto-start the server.

# Example: Slack MCP with auto-start server
# [mcps.slack]
# url = "http://localhost:30000/mcp/"
# transport = "http"
# description = "Slack 23+ tools (piekstra)"
# [mcps.slack.headers]
#   Authorization = "Bearer xoxb-your-token"
# [mcps.slack.server]
#   command = "uvx"
#   args = ["--python", "3.12", "slack-mcp-server", "--port", "30000"]
#   startup_timeout = 5000
#   health_check = "http://localhost:30000/health"
#   [mcps.slack.server.env]
#     SLACK_API_TOKEN = "xoxb-your-token"

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

# Example: Custom tool with inline env vars (appears in command picker)
# [tools.glm]
# command = "claude"
# icon = "🧠"
# dangerous_mode = true
# dangerous_flag = "--dangerously-skip-permissions"
# env = { ANTHROPIC_BASE_URL = "https://api.example.com/v4", API_KEY = "your-key" }

# ============================================================================
# Status Detection Pattern Overrides (Advanced)
# ============================================================================
# Built-in tools (claude, gemini, opencode, codex) have default detection
# patterns that work out of the box. You can extend them with *_extra fields
# (appended to defaults) or replace them entirely with the base fields.
# Patterns prefixed with "re:" are compiled as regex.
#
# Extend defaults (recommended):
# [tools.claude]
# busy_patterns_extra = ["my custom busy text", "re:custom.*regex"]
# prompt_patterns_extra = ["Custom>"]
# spinner_chars_extra = ["@"]
#
# Replace all defaults (use with caution):
# [tools.claude]
# busy_patterns = ["only-this-pattern"]
`

	// Add platform-aware MCP pool section
	exampleConfig += getMCPPoolConfigSection()

	// Ensure directory exists
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	return os.WriteFile(configPath, []byte(exampleConfig), 0o600)
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

// GetMCPDefaultScope returns the configured default MCP scope.
// Returns "local", "global", or "user". Defaults to "local" if unset or invalid.
func GetMCPDefaultScope() string {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return "local"
	}
	switch config.MCPDefaultScope {
	case "global", "user":
		return config.MCPDefaultScope
	default:
		return "local"
	}
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
