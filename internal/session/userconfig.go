package session

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BurntSushi/toml"

	dark "github.com/thiagokokada/dark-mode-go"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/platform"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// UserConfigFileName is the TOML config file for user preferences
const UserConfigFileName = "config.toml"

// UserConfig represents user-facing configuration in TOML format
type UserConfig struct {
	// DefaultTool is the pre-selected AI tool when creating new sessions
	// Valid values: "claude", "gemini", "opencode", "codex", "pi", or any custom tool name
	// If empty or invalid, defaults to "shell" (no pre-selection)
	DefaultTool string `toml:"default_tool"`

	// Hotkeys overrides default keyboard shortcuts in the TUI.
	// Keys are action names, values are key bindings (e.g., "delete" = "backspace").
	// Set an action to "" to explicitly unbind it.
	Hotkeys map[string]string `toml:"hotkeys"`

	// Theme sets the color scheme: "dark" (default), "light", or "system"
	Theme string `toml:"theme"`

	// Tools defines custom AI tool configurations
	Tools map[string]ToolDef `toml:"tools"`

	// MCPDefaultScope sets the default scope for MCP operations
	// Valid values: "local" (default), "global", "user"
	MCPDefaultScope string `toml:"mcp_default_scope"`

	// ManageMCPJson controls whether agent-deck writes to .mcp.json in project directories.
	// Set to false to prevent agent-deck from touching any .mcp.json files, which is useful
	// when you manage that file manually or via another tool.
	// Default: true (nil = true)
	ManageMCPJson *bool `toml:"manage_mcp_json"`

	// MCPs defines available MCP servers for the MCP Manager
	// These can be attached/detached per-project via the MCP Manager (M key)
	MCPs map[string]MCPDef `toml:"mcps"`

	// Claude defines Claude Code integration settings
	Claude ClaudeSettings `toml:"claude"`

	// Profiles defines optional per-profile overrides.
	// Example:
	// [profiles.work.claude]
	// config_dir = "~/.claude-work"
	Profiles map[string]ProfileSettings `toml:"profiles"`

	// Groups defines optional per-group overrides.
	// Example:
	// [groups."my-group".claude]
	// config_dir = "~/.claude-my-group"
	Groups map[string]GroupSettings `toml:"groups"`

	// Conductors defines optional per-conductor overrides.
	// Keyed by conductor name (matches Instance.Title minus "conductor-" prefix).
	// Mirrors Groups — see ConductorOverrides for the sub-table shape.
	// Closes issue #602.
	// Example:
	// [conductors.gsd-v154.claude]
	// config_dir = "~/.claude-work"
	// env_file   = "~/git/work/.envrc"
	Conductors map[string]ConductorOverrides `toml:"conductors"`

	// Gemini defines Gemini CLI integration settings
	Gemini GeminiSettings `toml:"gemini"`

	// OpenCode defines OpenCode CLI integration settings
	OpenCode OpenCodeSettings `toml:"opencode"`

	// Codex defines Codex CLI integration settings
	Codex CodexSettings `toml:"codex"`

	// Copilot defines GitHub Copilot CLI integration settings (Issue #556)
	Copilot CopilotSettings `toml:"copilot"`

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

	// Docker defines Docker sandbox settings for containerized sessions
	Docker DockerSettings `toml:"docker"`

	// Remotes defines named SSH remote agent-deck instances
	Remotes map[string]RemoteConfig `toml:"remotes"`

	// OpenClaw defines OpenClaw gateway integration settings
	OpenClaw OpenClawSettings `toml:"openclaw"`

	// Display defines rendering and display settings
	Display DisplaySettings `toml:"display"`

	// Costs defines cost tracking and budget settings
	Costs CostsSettings `toml:"costs"`

	// SystemStats defines system stats display settings (CPU, RAM, etc.)
	SystemStats SystemStatsSettings `toml:"system_stats"`

	// Watcher defines event watcher settings
	Watcher WatcherSettings `toml:"watcher"`

	// Feedback defines in-product feedback prompt settings (v1.7.38+).
	// Mirrors the opt-out in ~/.agent-deck/feedback-state.json so it is visible
	// to the user and editable without running `agent-deck feedback`.
	Feedback FeedbackSettings `toml:"feedback"`

	// Terminal defines outer-terminal chrome settings — sequences agent-deck
	// writes directly to the host terminal (iTerm2 badge, etc), distinct
	// from anything tmux draws. Empty/absent uses defaults; see TerminalSettings.
	Terminal TerminalSettings `toml:"terminal"`

	// Web defines `agent-deck web` HTTP server settings.
	Web WebSettings `toml:"web"`
}

// WebSettings configures the `agent-deck web` HTTP server.
type WebSettings struct {
	// MutationsEnabled controls whether POST/PATCH/DELETE endpoints accept
	// requests. nil (omitted) defaults to true. Forced off by --read-only.
	MutationsEnabled *bool `toml:"mutations_enabled"`
}

// FeedbackSettings controls the in-product feedback prompts.
// When Disabled is true, neither the auto-prompt (TUI) nor the post-launch
// auto-trigger (CLI, if any) will fire. Explicit `agent-deck feedback`
// invocations still run but show a re-enable prompt first. v1.7.38+.
type FeedbackSettings struct {
	// Disabled suppresses all passive feedback prompts when true.
	// Defaults to false. Set by RecordOptOut paths; cleared on re-enable.
	Disabled bool `toml:"disabled"`
}

// OpenClawSettings configures the OpenClaw gateway connection.
type OpenClawSettings struct {
	// GatewayURL is the WebSocket URL of the OpenClaw gateway (default: "ws://127.0.0.1:31337")
	GatewayURL string `toml:"gateway_url"`

	// Password is the gateway authentication password.
	// Supports env var references (e.g. "$OPENCLAW_PASSWORD" or "${OPENCLAW_PASSWORD}").
	// Falls back to OPENCLAW_PASSWORD env var if not set.
	Password string `toml:"password"`

	// AutoSync syncs OpenClaw agents as agent-deck sessions on TUI startup
	AutoSync bool `toml:"auto_sync"`

	// GroupName is the agent-deck group name for OpenClaw sessions (default: "openclaw")
	GroupName string `toml:"group_name"`
}

// RemoteConfig defines a remote agent-deck instance accessible via SSH.
type RemoteConfig struct {
	// Host is the SSH destination (e.g., "user@host" or "user@host:port")
	Host string `toml:"host"`

	// AgentDeckPath is the path to agent-deck binary on the remote (default: "agent-deck")
	AgentDeckPath string `toml:"agent_deck_path"`

	// Profile is the remote profile to use (default: "default")
	Profile string `toml:"profile"`
}

// GetAgentDeckPath returns the agent-deck binary path, defaulting to "agent-deck".
func (rc RemoteConfig) GetAgentDeckPath() string {
	if rc.AgentDeckPath != "" {
		return rc.AgentDeckPath
	}
	return "agent-deck"
}

// GetProfile returns the remote profile, defaulting to "default".
func (rc RemoteConfig) GetProfile() string {
	if rc.Profile != "" {
		return rc.Profile
	}
	return "default"
}

// ProfileSettings defines per-profile configuration overrides.
type ProfileSettings struct {
	// Claude defines Claude Code overrides for a specific profile.
	Claude ProfileClaudeSettings `toml:"claude"`
}

// ProfileClaudeSettings defines profile-specific Claude overrides.
type ProfileClaudeSettings struct {
	// ConfigDir overrides [claude].config_dir for this profile only.
	ConfigDir string `toml:"config_dir"`
}

// GroupSettings defines per-group configuration overrides.
type GroupSettings struct {
	// Claude defines Claude Code overrides for a specific group.
	Claude GroupClaudeSettings `toml:"claude"`
}

// GroupClaudeSettings defines group-specific Claude overrides.
type GroupClaudeSettings struct {
	// ConfigDir overrides [claude].config_dir for sessions in this group.
	ConfigDir string `toml:"config_dir"`

	// EnvFile overrides [claude].env_file for sessions in this group.
	EnvFile string `toml:"env_file"`
}

// ConductorOverrides defines per-conductor configuration overrides.
// Mirrors GroupSettings — conductors are first-class entities keyed by
// conductor name (derived from Instance.Title via strings.TrimPrefix at the
// call site, same pattern as env.go getConductorEnv).
//
// Named ConductorOverrides (not ConductorSettings) to avoid collision with
// the pre-existing global [conductor] meta-agent orchestration block
// declared in conductor.go:49 (heartbeat, telegram, slack, discord).
// Closes issue #602.
type ConductorOverrides struct {
	// Claude defines Claude Code overrides for a specific conductor.
	Claude ConductorClaudeSettings `toml:"claude"`
}

// ConductorClaudeSettings defines conductor-specific Claude overrides.
// Semantics mirror GroupClaudeSettings — ExpandPath is applied on read via
// GetConductorClaudeConfigDir; env_file resolution is deferred to the spawn
// builder (resolvePath handles path expansion at use).
type ConductorClaudeSettings struct {
	// ConfigDir overrides [claude].config_dir for this conductor only.
	ConfigDir string `toml:"config_dir"`

	// EnvFile is sourced before claude exec for this conductor.
	// Matches CFG-03 semantics — missing file logs a warning, does not block.
	EnvFile string `toml:"env_file"`
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

	// ShowNotes shows session notes section in preview pane
	// Default: false (pointer to distinguish "not set" from "explicitly true")
	ShowNotes *bool `toml:"show_notes"`

	// Analytics configures which sections to show in the analytics panel
	Analytics AnalyticsDisplaySettings `toml:"analytics"`

	// NotesOutputSplit controls vertical space allocation between notes and output
	// in the preview pane when output is visible.
	// Range: 0.1 - 0.9 (fraction reserved for notes). Default: 0.33
	NotesOutputSplit float64 `toml:"notes_output_split"`
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

	// ShowAll displays all sessions (with status icons) instead of only waiting sessions (default: false)
	ShowAll bool `toml:"show_all"`

	// Minimal shows a compact icon+count summary instead of session names: ● 2 │ ◐ 3 │ ○ 1
	// When true, key bindings (Ctrl+b 1-6) are disabled. ShowAll is ignored. (default: false)
	Minimal bool `toml:"minimal"`

	// TransitionEvents controls whether the transition daemon sends tmux messages
	// to parent sessions when a child transitions (e.g., running → waiting).
	// Default: true (nil = true). Set to false to suppress dispatch globally.
	// Per-session override: Instance.NoTransitionNotify
	TransitionEvents *bool `toml:"transition_events"`
}

// GetTransitionEventsEnabled returns whether transition event dispatch is enabled.
// Defaults to true when unset (nil).
func (n NotificationsConfig) GetTransitionEventsEnabled() bool {
	if n.TransitionEvents == nil {
		return true
	}
	return *n.TransitionEvents
}

// InstanceSettings configures multiple agent-deck instance behavior
type InstanceSettings struct {
	// AllowMultiple allows running multiple agent-deck TUI instances for the same profile
	// When true (default), multiple instances can run, but only the first (primary) manages the notification bar
	// When false, only one instance can run per profile
	AllowMultiple *bool `toml:"allow_multiple"`

	// FollowCwdOnAttach updates the session's ProjectPath from tmux pane_current_path
	// after returning from attach, and persists the new path.
	// Default: false
	FollowCwdOnAttach *bool `toml:"follow_cwd_on_attach"`
}

// GetAllowMultiple returns whether multiple instances are allowed, defaulting to true
func (i *InstanceSettings) GetAllowMultiple() bool {
	if i.AllowMultiple == nil {
		return true // Default: allow multiple instances (better UX for multi-pane workflows)
	}
	return *i.AllowMultiple
}

// GetFollowCwdOnAttach returns whether attach-return CWD follow is enabled.
func (i *InstanceSettings) GetFollowCwdOnAttach() bool {
	if i.FollowCwdOnAttach == nil {
		return false
	}
	return *i.FollowCwdOnAttach
}

// ShellSettings defines shell environment configuration for sessions
type ShellSettings struct {
	// EnvFiles is a list of .env files to source for ALL sessions
	// Paths can be absolute, ~ for home, $HOME/${VAR} for env vars, or relative to session working directory
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

// GetShowNotes returns whether to show notes section, defaulting to false
func (p *PreviewSettings) GetShowNotes() bool {
	if p.ShowNotes == nil {
		return false // Default: notes OFF
	}
	return *p.ShowNotes
}

// GetNotesOutputSplit returns notes/output split ratio, clamped to sane bounds.
func (p *PreviewSettings) GetNotesOutputSplit() float64 {
	if p.NotesOutputSplit <= 0 {
		return 0.33
	}
	if p.NotesOutputSplit < 0.1 {
		return 0.1
	}
	if p.NotesOutputSplit > 0.9 {
		return 0.9
	}
	return p.NotesOutputSplit
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

// GetShowNotes returns whether to show notes section, defaulting to false
func (c *UserConfig) GetShowNotes() bool {
	return c.Preview.GetShowNotes()
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

	// AutoMode enables --permission-mode auto flag for Claude sessions
	// A classifier model reviews commands before they run, blocking scope escalation
	// and hostile-content-driven actions while letting routine work proceed without prompts.
	// Ignored when dangerous_mode is true (the stronger flag takes precedence).
	// Default: false
	AutoMode bool `toml:"auto_mode"`

	// EnvFile is a .env file specific to Claude sessions
	// Sourced AFTER global [shell].env_files
	// Path can be absolute, ~ for home, $HOME/${VAR} for env vars, or relative to session working directory
	EnvFile string `toml:"env_file"`

	// HooksEnabled enables Claude Code hooks for real-time status detection.
	// When enabled, agent-deck uses lifecycle hooks (SessionStart, Stop, etc.)
	// for instant, deterministic status updates instead of polling tmux content.
	// Default: true (nil = use default true, set false to disable)
	HooksEnabled *bool `toml:"hooks_enabled"`
}

// GetProfileClaudeConfigDir returns the profile-specific Claude config directory, if configured.
func (c *UserConfig) GetProfileClaudeConfigDir(profile string) string {
	if c == nil || profile == "" || c.Profiles == nil {
		return ""
	}
	profileCfg, ok := c.Profiles[profile]
	if !ok || profileCfg.Claude.ConfigDir == "" {
		return ""
	}
	return ExpandPath(profileCfg.Claude.ConfigDir)
}

// GetGroupClaudeConfigDir returns the group-specific Claude config directory, if configured.
func (c *UserConfig) GetGroupClaudeConfigDir(groupPath string) string {
	if c == nil || groupPath == "" || c.Groups == nil {
		return ""
	}
	groupCfg, ok := c.Groups[groupPath]
	if !ok || groupCfg.Claude.ConfigDir == "" {
		return ""
	}
	return ExpandPath(groupCfg.Claude.ConfigDir)
}

// GetGroupClaudeEnvFile returns the group-specific Claude env file, if configured.
func (c *UserConfig) GetGroupClaudeEnvFile(groupPath string) string {
	if c == nil || groupPath == "" || c.Groups == nil {
		return ""
	}
	groupCfg, ok := c.Groups[groupPath]
	if !ok || groupCfg.Claude.EnvFile == "" {
		return ""
	}
	return groupCfg.Claude.EnvFile
}

// GetConductorClaudeConfigDir returns the conductor-specific Claude config
// directory, if configured. Keyed by conductor name (Instance.Title minus
// "conductor-" prefix — single source of truth is conductorNameFromInstance
// in claude.go). Path expansion matches GetGroupClaudeConfigDir. Returns ""
// when the conductor has no block or no config_dir — callers fall through
// to the group/profile/global chain.
func (c *UserConfig) GetConductorClaudeConfigDir(name string) string {
	if c == nil || name == "" || c.Conductors == nil {
		return ""
	}
	conductorCfg, ok := c.Conductors[name]
	if !ok || conductorCfg.Claude.ConfigDir == "" {
		return ""
	}
	return ExpandPath(conductorCfg.Claude.ConfigDir)
}

// GetConductorClaudeEnvFile returns the conductor-specific Claude env_file,
// if configured. Mirrors GetGroupClaudeEnvFile — no expansion here;
// resolvePath handles it at the spawn-command build site (env.go).
func (c *UserConfig) GetConductorClaudeEnvFile(name string) string {
	if c == nil || name == "" || c.Conductors == nil {
		return ""
	}
	conductorCfg, ok := c.Conductors[name]
	if !ok || conductorCfg.Claude.EnvFile == "" {
		return ""
	}
	return conductorCfg.Claude.EnvFile
}

// GetDangerousMode returns whether dangerous mode is enabled, defaulting to true
// Power users (the primary audience) typically want this enabled for faster iteration
func (c *ClaudeSettings) GetDangerousMode() bool {
	if c.DangerousMode == nil {
		return true
	}
	return *c.DangerousMode
}

// GetHooksEnabled returns whether Claude Code hooks are enabled, defaulting to true
func (c *ClaudeSettings) GetHooksEnabled() bool {
	if c.HooksEnabled == nil {
		return true
	}
	return *c.HooksEnabled
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
	// Path can be absolute, ~ for home, $HOME/${VAR} for env vars, or relative to session working directory
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
	// Path can be absolute, ~ for home, $HOME/${VAR} for env vars, or relative to session working directory
	EnvFile string `toml:"env_file"`
}

// CodexSettings defines Codex CLI configuration
type CodexSettings struct {
	// YoloMode enables --yolo flag for Codex sessions (bypass approvals and sandbox)
	// Default: false
	YoloMode bool `toml:"yolo_mode"`
}

// CopilotSettings defines GitHub Copilot CLI configuration (Issue #556).
// Binary: `copilot` from @github/copilot (GA 2026-02-25).
// Doc: https://docs.github.com/en/copilot/concepts/agents/about-copilot-cli
type CopilotSettings struct {
	// EnvFile is a .env file specific to Copilot sessions (sourced before
	// the `copilot` command runs, like [gemini].env_file). Optional.
	EnvFile string `toml:"env_file"`
}

// WorktreeSettings contains git worktree preferences.
type WorktreeSettings struct {
	// AutoCleanup: remove worktree when session is deleted
	AutoCleanup bool `toml:"auto_cleanup"`

	// DefaultEnabled controls whether worktree creation is pre-selected in
	// new-session and fork dialogs by default.
	// Default: false
	DefaultEnabled bool `toml:"default_enabled"`

	// DefaultLocation: "sibling" (next to repo), "subdirectory" (inside .worktrees/),
	// or a custom path (e.g., "~/worktrees") creating <path>/<repo_name>/<branch>
	DefaultLocation string `toml:"default_location"`

	// PathTemplate: custom path template for worktree location.
	// Variables:
	//   {repo-name}, {repo-root}, {session-id}
	//   {branch}         -> sanitized (human-friendly, may collide)
	//   {branch-escaped} -> URL-escaped (collision-resistant, reversible)
	// Unknown variables like {foo} are left as-is in the path.
	// If set, overrides DefaultLocation.
	PathTemplate *string `toml:"path_template"`

	// BranchPrefix is the prefix for auto-generated branch names when creating
	// worktree sessions. For example, "feature/" produces "feature/my-session".
	// Set to "" to disable auto-prefixing (just the session name).
	// Default: "feature/" when not set.
	BranchPrefix *string `toml:"branch_prefix"`

	// SetupTimeoutSeconds caps how long .agent-deck/worktree-setup.sh may run.
	// Pointer (not plain int) so the loader can distinguish three cases:
	//   nil         → field unset → 60s default (backward compat, GH #724)
	//   *0          → explicit unlimited (no deadline) — #727 follow-up
	//   *N (N > 0)  → N seconds
	//   *N (N < 0)  → treated as unset (60s default)
	// The `*0 = unlimited` convention matches standard CLI tooling (curl,
	// systemd, docker). Reporter @Clindbergh flagged the v1.7.65 behaviour
	// (`0 = default`) as counter-convention in the PR review for #727.
	SetupTimeoutSeconds *int `toml:"setup_timeout_seconds"`
}

// DefaultWorktreeSetupTimeout is the fallback used when no explicit value is
// configured. Kept small and visible so the git package can share it.
const DefaultWorktreeSetupTimeout = 60 * time.Second

// UnlimitedWorktreeSetupTimeout is the sentinel returned by SetupTimeout()
// when the user has configured `setup_timeout_seconds = 0`. The git layer
// interprets this as "no deadline" (context.Background() instead of
// context.WithTimeout). Value chosen as 0 so the config value flows straight
// through to the git layer unchanged.
const UnlimitedWorktreeSetupTimeout time.Duration = 0

// SetupTimeout returns the configured worktree-setup-script timeout.
// Semantics (post-#727 follow-up):
//   - field unset (nil) or negative → DefaultWorktreeSetupTimeout (60s)
//   - explicit 0                    → UnlimitedWorktreeSetupTimeout (no deadline)
//   - positive N                    → N seconds
func (w WorktreeSettings) SetupTimeout() time.Duration {
	if w.SetupTimeoutSeconds == nil {
		return DefaultWorktreeSetupTimeout
	}
	v := *w.SetupTimeoutSeconds
	if v < 0 {
		return DefaultWorktreeSetupTimeout
	}
	if v == 0 {
		return UnlimitedWorktreeSetupTimeout
	}
	return time.Duration(v) * time.Second
}

// Template returns the path template if set, or empty string if nil.
func (w *WorktreeSettings) Template() string {
	if w.PathTemplate == nil {
		return ""
	}
	return *w.PathTemplate
}

// Prefix returns the branch prefix if set, or "feature/" if nil.
// Environment variables (e.g., $USER) in the prefix are expanded.
func (w *WorktreeSettings) Prefix() string {
	if w.BranchPrefix == nil {
		return "feature/"
	}
	return os.ExpandEnv(*w.BranchPrefix)
}

// ApplyBranchPrefix prepends the configured prefix to a branch name.
// If the branch name already starts with the expanded prefix, it is returned unchanged.
func (w *WorktreeSettings) ApplyBranchPrefix(branch string) string {
	prefix := w.Prefix()
	if prefix == "" || strings.HasPrefix(branch, prefix) {
		return branch
	}
	return prefix + branch
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

	// CompatibleWith opts this tool into compatibility behavior for a built-in
	// tool even when the configured command is a wrapper script rather than the
	// literal executable name. Supported values currently include "claude" and
	// "codex".
	CompatibleWith string `toml:"compatible_with"`

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
	// Path can be absolute, ~ for home, $HOME/${VAR} for env vars, or relative to session working directory
	EnvFile string `toml:"env_file"`

	// Env is inline environment variables for this tool
	// These are exported AFTER env_file (highest priority)
	// Example: env = { ANTHROPIC_BASE_URL = "https://...", API_KEY = "token" }
	Env map[string]string `toml:"env"`

	// Pattern override fields (extend built-in defaults for claude/gemini/opencode/codex/pi)
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
//	inject_status_line = false
//	options = { "allow-passthrough" = "all", "history-limit" = "50000" }
type TmuxSettings struct {
	// InjectStatusLine controls whether agent-deck injects a custom status line
	// into new tmux sessions. When false, the tmux status bar is not modified,
	// allowing users to use their own tmux status line configuration. This also
	// disables Agent Deck's global tmux notification bar and key bindings so the
	// runtime stops mutating global tmux options.
	// Default: true (nil = use default true)
	InjectStatusLine *bool `toml:"inject_status_line"`

	// Mouse controls whether agent-deck enables tmux mouse mode on new
	// sessions. When false, tmux `mouse on` is never set, so the terminal
	// emulator keeps raw control of mouse events — required by the VS Code
	// Linux integrated terminal to let users click-drag to select text
	// (issue #730). Affects both the inline set-option during session
	// creation and the separate EnableMouseMode() path used on reconnect.
	// Default: true (nil = use default true, preserves pre-#730 behavior)
	Mouse *bool `toml:"mouse"`

	// LaunchInUserScope starts new tmux servers via `systemd-run --user --scope`
	// so the tmux server lives under the user's systemd manager instead of the
	// current login session scope. This keeps tmux alive when an SSH session
	// scope is torn down.
	//
	// Default (when nil / field absent): true on Linux hosts where
	// `systemd-run --user --version` succeeds, false otherwise. Explicit
	// `launch_in_user_scope = true` or `launch_in_user_scope = false` in
	// config.toml is always honored. Pointer type is required to distinguish
	// "field absent" from "explicit false".
	LaunchInUserScope *bool `toml:"launch_in_user_scope"`

	// LaunchAs selects the spawn form for new tmux servers (v1.7.21+).
	// Valid values (case-insensitive, whitespace-trimmed):
	//   "scope"   — systemd-run --user --scope (PR #467 legacy behavior)
	//   "service" — systemd-run --user --unit <NAME>.service with
	//               Type=forking + Restart=on-failure. Adds auto-restart
	//               if the tmux daemon dies unexpectedly (OOM, SIGKILL,
	//               kernel signal). Opt-in defense-in-depth.
	//   "direct"  — plain `tmux new-session` (no systemd isolation).
	//   "auto"    — service where systemd-user manager is available,
	//               else direct.
	//   ""        — unset (default): defer to LaunchInUserScope.
	//
	// LaunchAs, when non-empty and valid, takes precedence over
	// LaunchInUserScope. Unknown values are ignored (fall through to
	// LaunchInUserScope) so a config typo doesn't silently opt the user
	// onto an unintended spawn path.
	//
	// This is additive — v1.7.20 users get zero behavior change until
	// they explicitly set launch_as.
	LaunchAs *string `toml:"launch_as"`

	// WindowStyleOverride sets the tmux window-style (and window-active-style) for
	// all sessions, overriding the theme default. Use "default" to let your terminal
	// emulator's background show through instead of agent-deck's theme color.
	// Empty string (default) means use the theme's built-in value.
	// Takes precedence over the same keys in Options if both are set.
	// Example: window_style_override = "default"
	WindowStyleOverride string `toml:"window_style_override"`

	// ClearOnRestart clears the tmux scrollback buffer when a session is
	// restarted (respawn-pane). When false (default), the previous session's
	// output is preserved in scrollback. When true, scrollback is wiped so
	// the new session starts with a clean buffer.
	ClearOnRestart bool `toml:"clear_on_restart"`

	// DetachKey overrides the PTY-attach detach key (issue #434). Accepts
	// the same lowercase "ctrl+<letter>" form as `[hotkeys].detach` (e.g.
	// "ctrl+d"). When set to a non-empty string, it becomes an alias for
	// `[hotkeys].detach`. Precedence: explicit `[hotkeys].detach` always
	// wins; `[tmux].detach_key` is used only when `[hotkeys].detach` is
	// absent. Empty string (default) preserves the built-in Ctrl+Q.
	//
	// Why the alias exists: #434 reporters asked for a `[tmux]` section
	// entry because they think of the detach as a tmux-attach concern.
	// Keeping `[hotkeys].detach` authoritative avoids two sources of truth.
	DetachKey string `toml:"detach_key"`

	// Options is a map of tmux option names to values.
	// These are passed to `tmux set-option -t <session>` after defaults.
	Options map[string]string `toml:"options"`

	// SocketName is the tmux `-L <name>` socket selector for every
	// agent-deck tmux spawn (v1.7.50+, issue #687). Empty string — the
	// default — keeps pre-v1.7.50 behavior byte-for-byte: agent-deck shares
	// the user's default tmux server at $TMUX_TMPDIR/tmux-<uid>/default.
	//
	// Set this to isolate agent-deck onto its own tmux server so:
	//   - `[tmux].inject_status_line`, bind-key, and global set-option
	//     mutations stay on the agent-deck server and never touch the
	//     user's interactive tmux config (the original #276 complaint);
	//   - a `tmux kill-server` in the user's shell can't take agent-deck's
	//     managed sessions down with it;
	//   - `tmux -L <name> ls` from the shell shows exactly agent-deck's
	//     sessions — no mixing with the user's own work sessions.
	//
	// Each Instance captures this value at creation time into
	// Instance.TmuxSocketName; changing socket_name later does NOT migrate
	// existing sessions (they remain reachable on their original socket
	// until explicitly re-created). See docs/SOCKET_ISOLATION.md for the
	// migration procedure.
	//
	// Precedence at Instance creation: CLI flag `--tmux-socket <name>`
	// wins, else this config value, else empty.
	SocketName string `toml:"socket_name"`
}

// GetInjectStatusLine returns whether to inject status line, defaulting to true.
func (t TmuxSettings) GetInjectStatusLine() bool {
	if t.InjectStatusLine == nil {
		return true
	}
	return *t.InjectStatusLine
}

// GetSocketName returns the trimmed `[tmux].socket_name` value, or "" when
// unset, whitespace-only, or absent. Centralising the trim here means
// every caller — tmux.SetDefaultSocketName at startup, CLI flag merging,
// Instance creation — sees the same sanitised value.
func (t TmuxSettings) GetSocketName() string {
	return strings.TrimSpace(t.SocketName)
}

// GetMouse returns whether tmux mouse mode should be enabled, defaulting to
// true. Issue #730: users on VS Code's Linux integrated terminal need mouse
// OFF so the terminal can handle click-drag selection natively.
func (t TmuxSettings) GetMouse() bool {
	if t.Mouse == nil {
		return true
	}
	return *t.Mouse
}

// GetLaunchInUserScope returns whether new tmux servers should be launched
// under the user's systemd manager. If LaunchInUserScope is non-nil
// (explicit override in config.toml), its value is returned. Otherwise the
// default is determined by isSystemdUserScopeAvailable(): true on
// Linux+systemd hosts, false elsewhere. PERSIST-01..PERSIST-03.
func (t TmuxSettings) GetLaunchInUserScope() bool {
	if t.LaunchInUserScope != nil {
		return *t.LaunchInUserScope
	}
	return isSystemdUserScopeAvailable()
}

// GetLaunchAs returns the canonicalised launch mode string parsed from
// config.toml's [tmux].launch_as key. Returns "" if the field is unset
// or contains an unknown value (in which case downstream callers fall
// back to LaunchInUserScope). v1.7.21+.
func (t TmuxSettings) GetLaunchAs() string {
	if t.LaunchAs == nil {
		return ""
	}
	v := strings.ToLower(strings.TrimSpace(*t.LaunchAs))
	switch v {
	case "scope", "service", "direct", "auto":
		return v
	default:
		return ""
	}
}

// systemdUserScopeAvailable caches the result of probing whether
// `systemd-run --user --version` succeeds on this host. Populated exactly
// once per process via systemdUserScopeOnce. Tests can reset both vars via
// resetSystemdDetectionCacheForTest.
//
// systemdUserScopeProbeCount counts how many times the probe body has run;
// it is incremented inside the sync.Once.Do callback. Tests assert it equals
// 1 after consecutive calls (cache hit) and 2 after a reset+call cycle.
var (
	systemdUserScopeOnce       sync.Once
	systemdUserScopeAvailable  bool
	systemdUserScopeProbeCount int64
)

// isSystemdUserScopeAvailable returns true iff exec.LookPath("systemd-run")
// succeeds AND `systemd-run --user --version` exits zero. The result is
// cached for the lifetime of the process. The probe must mirror
// requireSystemdRun in internal/session/session_persistence_test.go so the
// production-code default and the test gate agree on what "Linux+systemd
// available" means. Side effects: none — no stdout/stderr writes, no panic
// on missing/broken systemd-run, errors are swallowed and treated as false.
func isSystemdUserScopeAvailable() bool {
	systemdUserScopeOnce.Do(func() {
		atomic.AddInt64(&systemdUserScopeProbeCount, 1)
		if _, err := exec.LookPath("systemd-run"); err != nil {
			systemdUserScopeAvailable = false
			return
		}
		if err := exec.Command("systemd-run", "--user", "--version").Run(); err != nil {
			systemdUserScopeAvailable = false
			return
		}
		systemdUserScopeAvailable = true
	})
	return systemdUserScopeAvailable
}

// resetSystemdDetectionCacheForTest discards the cached detection result
// so the next call to isSystemdUserScopeAvailable re-probes the host. Used
// only by tests in package session. Not safe for concurrent use with
// callers of isSystemdUserScopeAvailable.
func resetSystemdDetectionCacheForTest() {
	systemdUserScopeOnce = sync.Once{}
	systemdUserScopeAvailable = false
}

// systemdAvailableForLog is a swappable seam so unit tests can deterministically
// drive both branches of the OBS-01 log decision without manipulating PATH or
// the systemd user manager. Production callers always read
// isSystemdUserScopeAvailable.
var systemdAvailableForLog = isSystemdUserScopeAvailable

// cgroupIsolationLog is the slog handle used by LogCgroupIsolationDecision.
// It mirrors the migrationLog pattern at internal/session/migration.go:13 so
// the OBS-01 line is routed through the same dynamicHandler that lands records
// in lumberjack-rotated ~/.agent-deck/debug.log. Tests swap it via
// captureCgroupIsolationLog to capture the emitted record without going
// through disk.
var cgroupIsolationLog *slog.Logger = logging.ForComponent(logging.CompSession)

// cgroupIsolationOnce ensures LogCgroupIsolationDecision emits exactly once
// per process. Tests can reset it via resetCgroupIsolationLogOnceForTest.
var cgroupIsolationOnce sync.Once

// resetCgroupIsolationLogOnceForTest clears the once-guard so the next
// LogCgroupIsolationDecision call re-emits. Test-only — never call from
// production code.
func resetCgroupIsolationLogOnceForTest() {
	cgroupIsolationOnce = sync.Once{}
}

// LogCgroupIsolationDecision emits exactly one structured log line per
// process describing the cgroup isolation decision the runtime made. The
// emitted message is one of these four exact strings (pinned by
// TestLogCgroupIsolationDecision_*):
//
//   - "tmux cgroup isolation: enabled (systemd-run detected)"
//   - "tmux cgroup isolation: disabled (systemd-run not available)"
//   - "tmux cgroup isolation: enabled (config override)"
//   - "tmux cgroup isolation: disabled (config override)"
//
// Decision logic mirrors GetLaunchInUserScope: an explicit (non-nil)
// LaunchInUserScope wins, otherwise systemdAvailableForLog() decides. The
// sync.Once guarantees one-line-per-process even when called from multiple
// goroutines.
//
// Satisfies OBS-01. Intended to be called once from the application bootstrap
// (cmd/agent-deck/main.go) immediately after logging.Init so the line lands in
// ~/.agent-deck/debug.log via lumberjack.
func LogCgroupIsolationDecision() {
	cgroupIsolationOnce.Do(func() {
		settings := GetTmuxSettings()
		var msg string
		switch {
		case settings.LaunchInUserScope != nil && *settings.LaunchInUserScope:
			msg = "tmux cgroup isolation: enabled (config override)"
		case settings.LaunchInUserScope != nil && !*settings.LaunchInUserScope:
			msg = "tmux cgroup isolation: disabled (config override)"
		case systemdAvailableForLog():
			msg = "tmux cgroup isolation: enabled (systemd-run detected)"
		default:
			msg = "tmux cgroup isolation: disabled (systemd-run not available)"
		}
		cgroupIsolationLog.Info(msg)
	})
}

// DockerSettings defines Docker sandbox configuration.
type DockerSettings struct {
	// DefaultImage is the sandbox image to use when not specified per-session.
	DefaultImage string `toml:"default_image"`

	// DefaultEnabled enables sandbox by default for new sessions.
	DefaultEnabled bool `toml:"default_enabled"`

	// CPULimit is the default CPU limit for sandboxed containers (e.g. "2.0").
	CPULimit string `toml:"cpu_limit"`

	// MemoryLimit is the default memory limit for sandboxed containers (e.g. "4g").
	MemoryLimit string `toml:"memory_limit"`

	// VolumeIgnores is a list of directories to exclude from the project mount.
	VolumeIgnores []string `toml:"volume_ignores"`

	// Environment lists host environment variable names whose values are forwarded to the
	// container at runtime via docker exec -e. The actual values are read from the host
	// on each command invocation, so changes take effect without recreating the container.
	Environment []string `toml:"environment"`

	// ExtraVolumes maps host paths to container paths for additional bind mounts.
	ExtraVolumes map[string]string `toml:"extra_volumes"`

	// EnvironmentValues are static key=value pairs baked into the container at creation
	// time via docker create -e. Unlike Environment (which forwards by name at runtime),
	// these are fixed when the container is created.
	EnvironmentValues map[string]string `toml:"environment_values"`

	// MountSSH mounts ~/.ssh read-only inside the container.
	MountSSH bool `toml:"mount_ssh"`

	// AutoCleanup removes sandbox containers on session kill (default: true).
	AutoCleanup *bool `toml:"auto_cleanup"`
}

// GetAutoCleanup returns whether to auto-remove sandbox containers, defaulting to true.
func (d DockerSettings) GetAutoCleanup() bool {
	if d.AutoCleanup == nil {
		return true
	}
	return *d.AutoCleanup
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

// DisplaySettings controls TUI rendering behavior.
type DisplaySettings struct {
	// FullRepaint forces a full screen clear on every render cycle instead of
	// incremental redraws. Enable this if you see vertical drift or rendering
	// artifacts in terminals that use unicode grapheme-cluster widths (e.g.
	// Ghostty 1.3+ with grapheme-width-method=unicode).
	// Can also be enabled via AGENTDECK_REPAINT=full env var.
	// Default: false
	FullRepaint bool `toml:"full_repaint"`

	// DefaultFilter sets the initial status filter when the TUI opens.
	// Valid values: "" (all, default), "active" (hides error/stopped),
	// "running", "waiting", "idle", "error".
	// If set to "active" and no non-error sessions exist, falls back to showing all.
	DefaultFilter string `toml:"default_filter"`

	// ActiveFilterLabel sets the label shown on the filter pill when the active
	// filter is engaged. Default: "Open". Examples: "Active", "Live", "Open".
	ActiveFilterLabel string `toml:"active_filter_label"`
}

// ValidDefaultFilters lists acceptable values for DefaultFilter.
var ValidDefaultFilters = map[string]bool{
	"":        true,
	"active":  true,
	"running": true,
	"waiting": true,
	"idle":    true,
	"error":   true,
}

// GetDefaultFilter returns the validated default_filter value, falling back to "" on invalid input.
func (d DisplaySettings) GetDefaultFilter() string {
	if ValidDefaultFilters[d.DefaultFilter] {
		return d.DefaultFilter
	}
	return ""
}

// GetFullRepaint returns whether full-repaint mode is active, checking
// the env var AGENTDECK_REPAINT=full as an override.
func (d DisplaySettings) GetFullRepaint() bool {
	if strings.EqualFold(os.Getenv("AGENTDECK_REPAINT"), "full") {
		return true
	}
	return d.FullRepaint
}

// Default user config (empty maps)
var defaultUserConfig = UserConfig{
	Tools: make(map[string]ToolDef),
	MCPs:  make(map[string]MCPDef),
}

// cloneDefaultUserConfig returns a fresh shallow copy of defaultUserConfig with
// independent Tools/MCPs maps, so callers mutating the returned value cannot
// leak into later LoadUserConfig() calls. Introduced with v1.7.38's feedback
// opt-out work after a cross-test mutation leak (cfg.Feedback.Disabled=true)
// corrupted the shared global between parallel test cases.
func cloneDefaultUserConfig() UserConfig {
	c := defaultUserConfig
	c.Tools = make(map[string]ToolDef, len(defaultUserConfig.Tools))
	for k, v := range defaultUserConfig.Tools {
		c.Tools[k] = v
	}
	c.MCPs = make(map[string]MCPDef, len(defaultUserConfig.MCPs))
	for k, v := range defaultUserConfig.MCPs {
		c.MCPs[k] = v
	}
	return c
}

// Cache for user config. Invalidated when config.toml's mtime advances past
// the snapshot taken at cache time, so long-running processes (TUI, web,
// notify-daemon) pick up external edits without requiring a full restart.
// Regression: TestLoadUserConfig_PicksUpExternalEdits.
var (
	userConfigCache      *UserConfig
	userConfigCacheMtime time.Time
	userConfigCacheMu    sync.RWMutex
)

// GetUserConfigPath returns the path to the user config file
func GetUserConfigPath() (string, error) {
	dir, err := GetAgentDeckDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, UserConfigFileName), nil
}

// LoadUserConfig loads the user configuration from TOML file.
// After the first load the result is cached; the cache is invalidated when
// config.toml's mtime advances, so external edits to the file (e.g. the user
// editing ~/.agent-deck/config.toml by hand while the TUI is running) are
// picked up on the next call without a manual ClearUserConfigCache.
func LoadUserConfig() (*UserConfig, error) {
	configPath, pathErr := GetUserConfigPath()

	// Stat the file once up front so both the fast-path and the slow-path
	// agree on the same mtime. A missing file is not an error — we fall
	// through to the default config branch below.
	var currentMtime time.Time
	if pathErr == nil {
		if st, err := os.Stat(configPath); err == nil {
			currentMtime = st.ModTime()
		}
	}

	userConfigCacheMu.RLock()
	if userConfigCache != nil && currentMtime.Equal(userConfigCacheMtime) {
		defer userConfigCacheMu.RUnlock()
		return userConfigCache, nil
	}
	userConfigCacheMu.RUnlock()

	userConfigCacheMu.Lock()
	defer userConfigCacheMu.Unlock()

	// Re-check under write lock: another goroutine may have refreshed the
	// cache to match currentMtime between our RLock drop and Lock acquire.
	if userConfigCache != nil && currentMtime.Equal(userConfigCacheMtime) {
		return userConfigCache, nil
	}

	if pathErr != nil {
		fresh := cloneDefaultUserConfig()
		userConfigCache = &fresh
		userConfigCacheMtime = time.Time{}
		return userConfigCache, nil
	}

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		fresh := cloneDefaultUserConfig()
		userConfigCache = &fresh
		userConfigCacheMtime = time.Time{}
		return userConfigCache, nil
	}

	var config UserConfig
	if _, err := toml.DecodeFile(configPath, &config); err != nil {
		// Cache default to prevent hot-looping on a broken file, but still
		// return the error so the caller can surface it.
		fresh := cloneDefaultUserConfig()
		userConfigCache = &fresh
		userConfigCacheMtime = currentMtime
		return userConfigCache, fmt.Errorf("config.toml parse error: %w", err)
	}

	if config.Tools == nil {
		config.Tools = make(map[string]ToolDef)
	}
	if config.MCPs == nil {
		config.MCPs = make(map[string]MCPDef)
	}

	userConfigCache = &config
	userConfigCacheMtime = currentMtime
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

// ClearUserConfigCache clears the cached user config, allowing tests to reset state.
// This does NOT reload - the next LoadUserConfig() call will read fresh from disk.
// Resets both the cache pointer AND the tracked mtime so the invalidation state
// machine starts clean.
func ClearUserConfigCache() {
	userConfigCacheMu.Lock()
	userConfigCache = nil
	userConfigCacheMtime = time.Time{}
	userConfigCacheMu.Unlock()
}

// IsClaudeCompatible returns true if the tool is "claude" or a custom tool
// whose underlying command is "claude". Use this for capability gates
// (session tracking, MCP, skills, hooks, etc.) where custom tools wrapping
// Claude should get full Claude functionality.
func IsClaudeCompatible(toolName string) bool {
	if toolName == "claude" {
		return true
	}
	if def := GetToolDef(toolName); def != nil {
		return strings.EqualFold(strings.TrimSpace(def.CompatibleWith), "claude") || isClaudeCommand(def.Command)
	}
	return false
}

// IsCodexCompatible returns true if the tool is "codex" or a custom tool
// whose underlying command is "codex". Use this for capability gates
// where custom tools wrapping Codex should get full Codex functionality
// without losing their configured tool identity.
func IsCodexCompatible(toolName string) bool {
	if toolName == "codex" {
		return true
	}
	if def := GetToolDef(toolName); def != nil {
		return strings.EqualFold(strings.TrimSpace(def.CompatibleWith), "codex") || isCodexCommand(def.Command)
	}
	return false
}

func isClaudeCommand(command string) bool {
	return isCommand(command, "claude")
}

func isCodexCommand(command string) bool {
	return isCommand(command, "codex")
}

func isCommand(command, wantBase string) bool {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return false
	}

	cmdToken := ""
	for _, field := range fields {
		if isShellEnvAssignment(field) {
			continue
		}
		cmdToken = strings.Trim(field, `"'`)
		break
	}
	if cmdToken == "" {
		return false
	}

	base := filepath.Base(cmdToken)
	base = strings.TrimSuffix(base, ".exe")
	base = strings.TrimSuffix(base, ".EXE")
	return strings.EqualFold(base, wantBase)
}

func isShellEnvAssignment(token string) bool {
	if token == "" {
		return false
	}
	idx := strings.IndexByte(token, '=')
	if idx <= 0 {
		return false
	}

	key := token[:idx]
	for i, r := range key {
		if i == 0 {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_') {
				return false
			}
			continue
		}
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			return false
		}
	}
	return true
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
// excluding names that shadow built-in tools (claude, gemini, opencode, codex, pi, shell, cursor, aider).
// Returns nil if no custom tools are configured.
func GetCustomToolNames() []string {
	config, err := LoadUserConfig()
	if err != nil || config == nil || len(config.Tools) == 0 {
		return nil
	}

	var names []string
	for name := range config.Tools {
		if !isBuiltinToolName(name) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	return names
}

func isBuiltinToolName(toolName string) bool {
	switch toolName {
	case "claude", "gemini", "opencode", "codex", "copilot", "pi", "shell", "cursor", "aider":
		return true
	default:
		return false
	}
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
	case "copilot":
		return "🐙"
	case "pi":
		return "π"
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

// GetWebMutationsEnabled returns whether `agent-deck web` should accept
// mutating HTTP requests (POST/PATCH/DELETE). Defaults to true when the
// `[web].mutations_enabled` key is omitted from config.toml.
func GetWebMutationsEnabled() bool {
	config, err := LoadUserConfig()
	if err != nil || config == nil || config.Web.MutationsEnabled == nil {
		return true
	}
	return *config.Web.MutationsEnabled
}

// GetHotkeyOverrides returns user-configured hotkey overrides from config.toml.
//
// Merge order (issue #434):
//  1. Start from the `[hotkeys]` table.
//  2. If `[tmux].detach_key` is set AND the caller has not already set
//     `[hotkeys].detach`, layer tmux.detach_key into the hotkeys map as the
//     "detach" action. Explicit `[hotkeys].detach` always wins so there is
//     exactly one authoritative source of truth when both are present.
//
// Returns nil only when nothing is configured in either table.
func GetHotkeyOverrides() map[string]string {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return nil
	}

	out := make(map[string]string, len(config.Hotkeys)+1)
	for action, key := range config.Hotkeys {
		out[action] = key
	}

	if tmuxKey := strings.TrimSpace(config.Tmux.DetachKey); tmuxKey != "" {
		if _, alreadySet := out[hotkeyDetachAction]; !alreadySet {
			out[hotkeyDetachAction] = tmuxKey
		}
	}

	if len(out) == 0 {
		return nil
	}
	return out
}

// hotkeyDetachAction is the canonical action name used by [hotkeys].detach.
// Duplicated from internal/ui/hotkeys.go::hotkeyDetach to avoid an import
// cycle (session <- ui). If the UI constant ever changes, update here too.
const hotkeyDetachAction = "detach"

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
	// Check the terminal's own declaration before asking the OS.
	// COLORFGBG is set by iTerm2 and other terminals; format is "fg;bg"
	// where bg < 8 means a dark background. This catches the common case
	// where macOS is in light mode but the terminal profile is dark.
	if colorfgbg := os.Getenv("COLORFGBG"); colorfgbg != "" {
		if idx := strings.LastIndex(colorfgbg, ";"); idx >= 0 {
			var bg int
			if _, err := fmt.Sscanf(colorfgbg[idx+1:], "%d", &bg); err == nil {
				if bg < 8 {
					return "dark"
				}
				return "light"
			}
		}
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
		settings.Directory = ExpandPath(settings.Directory)
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
			ShowAll:  false,
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
	// ShowAll defaults to false (backward compatible) - bool zero value handles this

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

// GetDockerSettings returns docker sandbox settings with defaults applied.
func GetDockerSettings() DockerSettings {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return DockerSettings{}
	}
	return config.Docker
}

// GetTmuxSettings returns tmux option overrides from config
func GetTmuxSettings() TmuxSettings {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return TmuxSettings{}
	}
	return config.Tmux
}

// TerminalSettings controls outer-terminal chrome agent-deck writes directly
// to the host terminal (bypassing tmux). These settings affect what the
// terminal emulator displays — currently only iTerm2's badge.
//
// Example config.toml:
//
//	[terminal]
//	iterm_badge = true
type TerminalSettings struct {
	// ITermBadge controls whether agent-deck sets the iTerm2 badge to the
	// attached session's title for the duration of the attach, and refreshes
	// it when Claude renames the session mid-attach. No-op outside iTerm2.
	//
	// AGENTDECK_ITERM_BADGE env var overrides this in either direction
	// (=1/true/yes/on force on, =0/false/no/off force off; unset defers to
	// this config). Caveat: env reliably reaches the attach/detach path
	// (agent-deck reads its own env directly) but the rename-while-attached
	// path runs in a hook subprocess spawned through agent-deck → tmux →
	// Claude → hook, and Claude may filter custom env vars. For consistent
	// behavior on both paths, prefer this config setting — every process
	// re-reads it from disk, so propagation is independent of the spawn
	// chain.
	//
	// Default: false (opt-in). Most users have their own iTerm2 badge scheme
	// (e.g. host/cwd via shell PROMPT_COMMAND), so silently overwriting it on
	// every attach is too presumptuous a default. Users who want the
	// per-session badge set this to true explicitly.
	ITermBadge *bool `toml:"iterm_badge"`
}

// GetITermBadge returns whether the iTerm2 badge integration is enabled,
// defaulting to false (opt-in). Mirrors the GetInjectStatusLine pattern but
// with the inverse default — see ITermBadge field doc for rationale.
func (t TerminalSettings) GetITermBadge() bool {
	if t.ITermBadge == nil {
		return false
	}
	return *t.ITermBadge
}

// GetTerminalSettings returns terminal-chrome settings from config.
func GetTerminalSettings() TerminalSettings {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return TerminalSettings{}
	}
	return config.Terminal
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
# Valid values: "claude", "gemini", "opencode", "codex", "pi", or any custom tool name
# Leave commented out or empty to default to shell (no pre-selection)
# default_tool = "claude"

# Hotkey overrides (optional)
# Action names are defined by agent-deck. Value is the key string.
# Set value to "" to unbind an action.
# [hotkeys]
# delete = "d"
# close_session = "D"
# restart = "R"
# detach = "ctrl+d"   # PTY-attach detach key, default ctrl+q (issue #434).
                      # Alias [tmux].detach_key exists; [hotkeys].detach wins.

# Attach-return project path sync (optional)
# [instances]
# follow_cwd_on_attach = true

# Preview settings (optional)
# [preview]
# show_notes = false
# notes_output_split = 0.33

# Claude Code integration
# [claude]
# Custom config directory (for dual account setups)
# Default: ~/.claude (or CLAUDE_CONFIG_DIR env var takes priority)
# config_dir = "~/.claude-work"
# Optional per-profile override (takes precedence over [claude] when profile matches)
# [profiles.work.claude]
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
# Pre-check "Create in worktree" in new-session and fork dialogs (default: false)
# default_enabled = true
# Automatically remove worktree when session is deleted
auto_cleanup = true
# Custom path template (overrides default_location if set)
# Variables:
#   {repo-name}, {repo-root}, {session-id}
#   {branch}         -> sanitized (human-friendly, may collide)
#   {branch-escaped} -> URL-escaped (collision-resistant, reversible)
# path_template = "../worktrees/{repo-name}/{branch}"

# Default scope for MCP operations: "local", "global", or "user"
# "local" writes to .mcp.json (project-only, default)
# "global" writes to Claude profile config (profile-wide)
# "user" writes to ~/.claude.json (all profiles)
# mcp_default_scope = "local"

# Disable ALL .mcp.json management (default: true)
# Set to false if you manage .mcp.json manually or via another tool and don't
# want agent-deck to touch it. LOCAL-scope MCP changes will be silently skipped.
# manage_mcp_json = false

# Tmux session settings
# Controls how agent-deck configures tmux sessions
# [tmux]
# inject_status_line controls whether agent-deck sets up a custom tmux status bar
# When false, your existing tmux status line configuration is preserved and
# agent-deck stops mutating the global tmux notification bar / number key bindings
# Default: true (agent-deck injects its own status bar with session info)
# inject_status_line = false
# mouse controls whether agent-deck enables tmux mouse mode.
# Set this to false if your terminal (e.g. VS Code's Linux integrated terminal)
# interprets mouse events at the terminal layer and you want click-drag text
# selection to bypass tmux entirely. Issue #730.
# Default: true (tmux mouse mode is enabled — scrolling, pane resize, selection in tmux)
# mouse = false
# launch_in_user_scope starts new tmux servers with systemd-run --user --scope
# so they survive when the current login session is torn down (e.g. SSH logout).
# Default: true on Linux+systemd hosts where 'systemd-run --user --version'
#          succeeds, false on macOS / BSD / Linux without a user manager.
# An explicit setting here is ALWAYS honored.
# launch_in_user_scope = false
# window_style_override sets the tmux window-style for all sessions, overriding
# the theme default. Use "default" to let your terminal's background show through.
# window_style_override = "default"
# clear_on_restart clears the tmux scrollback buffer when a session is restarted.
# When false (default), previous output is preserved. When true, scrollback is wiped.
# clear_on_restart = true
# detach_key overrides the PTY-attach detach key (default Ctrl+Q, issue #434).
# Same format as [hotkeys].detach — lowercase "ctrl+<letter>". Useful when your
# editor (e.g. Neovim) uses Ctrl+Q for another binding. [hotkeys].detach is the
# canonical source; [tmux].detach_key is an alias applied only when hotkeys.detach
# is absent. Both live options, documented so users find the one they look for.
# detach_key = "ctrl+d"
# Override tmux options applied to every session (applied after defaults).
# agent-deck does NOT set history-limit by default, so your tmux.conf value is used.
# Options matching agent-deck's managed keys (status, status-style,
# status-left-length, status-right, status-right-length) will cause agent-deck
# to skip its default for that key, letting your value take full effect.
# options = { "allow-passthrough" = "all", "history-limit" = "50000" }
# Example: keep agent-deck notifications but use a 2-line status bar
# options = { "status" = "2" }

# Outer-terminal chrome (sequences agent-deck writes to the host terminal,
# bypassing tmux). Currently controls the iTerm2 badge; future window-title
# integrations will live in the same section.
# [terminal]
# iterm_badge sets the iTerm2 badge to the attached session's title for the
# duration of the attach (cleared on detach), and refreshes it when Claude
# renames the session mid-attach. Opt-in because most users already drive
# the badge from their shell prompt. No-op outside iTerm2.
# Override at runtime: AGENTDECK_ITERM_BADGE=1 forces on, =0 forces off.
# Caveat: the env var reliably reaches the attach/detach path but is
# unreliable for rename-while-attached (Claude may filter env vars when
# spawning hook subprocesses). Prefer this config setting for both paths.
# Default: false
# iterm_badge = true

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
#   compatible_with - Built-in compatibility to mirror ("claude" or "codex")
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

# Example: Custom Codex wrapper that should restart and detect status like Codex
# [tools.my-codex]
# command = "codex-wrapper"
# compatible_with = "codex"
# icon = "C"

# ============================================================================
# Status Detection Pattern Overrides (Advanced)
# ============================================================================
# Built-in tools (claude, gemini, opencode, codex, pi) have default detection
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

// GetManageMCPJson returns whether agent-deck should write to .mcp.json files.
// Defaults to true when unset.
func GetManageMCPJson() bool {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return true
	}
	if config.ManageMCPJson == nil {
		return true
	}
	return *config.ManageMCPJson
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

// CostsSettings configures cost tracking, budgets, and pricing overrides.
type CostsSettings struct {
	Currency      string          `toml:"currency"`
	Timezone      string          `toml:"timezone"`
	RetentionDays int             `toml:"retention_days"`
	Budgets       BudgetSettings  `toml:"budgets"`
	Pricing       PricingSettings `toml:"pricing"`
}

type BudgetSettings struct {
	DailyLimit   float64                  `toml:"daily_limit"`
	WeeklyLimit  float64                  `toml:"weekly_limit"`
	MonthlyLimit float64                  `toml:"monthly_limit"`
	Groups       map[string]GroupBudget   `toml:"groups"`
	Sessions     map[string]SessionBudget `toml:"sessions"`
}

type GroupBudget struct {
	DailyLimit float64 `toml:"daily_limit"`
}

type SessionBudget struct {
	TotalLimit float64 `toml:"total_limit"`
}

type PricingSettings struct {
	Overrides map[string]PricingOverride `toml:"overrides"`
}

type PricingOverride struct {
	InputPerMtok      float64 `toml:"input_per_mtok"`
	OutputPerMtok     float64 `toml:"output_per_mtok"`
	CacheReadPerMtok  float64 `toml:"cache_read_per_mtok"`
	CacheWritePerMtok float64 `toml:"cache_write_per_mtok"`
}

func (c CostsSettings) GetRetentionDays() int {
	if c.RetentionDays > 0 {
		return c.RetentionDays
	}
	return 90
}

func (c CostsSettings) GetTimezone() string {
	if c.Timezone != "" {
		return c.Timezone
	}
	return "Local"
}

// SystemStatsSettings configures the system stats display in the status bar.
type SystemStatsSettings struct {
	// Enabled controls whether system stats are collected and displayed (default: true)
	Enabled *bool `toml:"enabled"`

	// RefreshSeconds sets the collection interval in seconds (default: 5, min: 2)
	RefreshSeconds int `toml:"refresh_seconds"`

	// Format controls display density: "compact" (icons), "full" (labels), "minimal" (values only)
	Format string `toml:"format"`

	// Show lists which stats to display: "cpu", "ram", "disk", "load", "gpu", "network"
	Show []string `toml:"show"`
}

// GetEnabled returns whether system stats display is enabled (default: true).
func (s SystemStatsSettings) GetEnabled() bool {
	if s.Enabled != nil {
		return *s.Enabled
	}
	return true
}

// GetRefreshSeconds returns the collection interval, clamped to [2, 300].
func (s SystemStatsSettings) GetRefreshSeconds() int {
	if s.RefreshSeconds >= 2 {
		if s.RefreshSeconds > 300 {
			return 300
		}
		return s.RefreshSeconds
	}
	return 5
}

// GetFormat returns the display format (default: "compact").
func (s SystemStatsSettings) GetFormat() string {
	switch s.Format {
	case "full", "minimal":
		return s.Format
	default:
		return "compact"
	}
}

// GetShow returns the list of stats to display. Defaults to cpu, ram, disk, network.
func (s SystemStatsSettings) GetShow() []string {
	if len(s.Show) > 0 {
		return s.Show
	}
	return []string{"cpu", "ram", "disk", "network"}
}

// WatcherSettings configures the event watcher system.
type WatcherSettings struct {
	// MaxEventsPerWatcher is the maximum number of events to retain per watcher (default: 500)
	MaxEventsPerWatcher int `toml:"max_events_per_watcher"`

	// MaxSilenceMinutes triggers a health warning when no events received (default: 60)
	MaxSilenceMinutes int `toml:"max_silence_minutes"`

	// HealthCheckIntervalSeconds is the interval between health checks in seconds (default: 30)
	HealthCheckIntervalSeconds int `toml:"health_check_interval_seconds"`

	// Alerts configures the health alerts bridge (opt-in). See WatcherAlertsSettings.
	Alerts WatcherAlertsSettings `toml:"alerts"`
}

// GetMaxEventsPerWatcher returns the max events per watcher (default: 500).
func (w WatcherSettings) GetMaxEventsPerWatcher() int {
	if w.MaxEventsPerWatcher > 0 {
		return w.MaxEventsPerWatcher
	}
	return 500
}

// GetMaxSilenceMinutes returns the silence threshold in minutes (default: 60).
func (w WatcherSettings) GetMaxSilenceMinutes() int {
	if w.MaxSilenceMinutes > 0 {
		return w.MaxSilenceMinutes
	}
	return 60
}

// GetHealthCheckIntervalSeconds returns the health check interval in seconds (default: 30).
func (w WatcherSettings) GetHealthCheckIntervalSeconds() int {
	if w.HealthCheckIntervalSeconds > 0 {
		return w.HealthCheckIntervalSeconds
	}
	return 30
}

// WatcherAlertsSettings configures the health alerts bridge (REQ-WF-3).
// Opt-in via [watcher.alerts] in config.toml.
type WatcherAlertsSettings struct {
	// Enabled turns the bridge on. Default: false (no alerts emitted).
	Enabled bool `toml:"enabled"`

	// Channels lists notification channel names the bridge's notifier should fan out to
	// (e.g. "telegram", "slack", "discord"). Semantics are owned by the Notifier
	// implementation; the bridge only passes the list to the notifier.
	Channels []string `toml:"channels"`

	// DebounceMinutes is the per-(watcher x trigger) debounce window. Default: 15.
	DebounceMinutes int `toml:"debounce_minutes"`
}

// GetDebounceMinutes returns the debounce window in minutes (default: 15).
func (a WatcherAlertsSettings) GetDebounceMinutes() int {
	if a.DebounceMinutes > 0 {
		return a.DebounceMinutes
	}
	return 15
}
