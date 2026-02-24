package session

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

var sessionLog = logging.ForComponent(logging.CompSession)
var mcpLog = logging.ForComponent(logging.CompMCP)

// Status represents the current state of a session
type Status string

const (
	StatusRunning  Status = "running"
	StatusWaiting  Status = "waiting"
	StatusIdle     Status = "idle"
	StatusError    Status = "error"
	StatusStarting Status = "starting" // Session is being created (tmux initializing)
)

const wrapperPlaceholder = "{command}"

const (
	hookFastPathWindow             = 2 * time.Minute
	codexHookRunningFastPathWindow = 20 * time.Second
	codexHookWaitingFastPathWindow = 2 * time.Minute
	codexBootstrapScanInterval     = 2 * time.Second
	codexRotationScanInterval      = 30 * time.Second
)

// Instance represents a single agent/shell session
type Instance struct {
	ID                string `json:"id"`
	Title             string `json:"title"`
	ProjectPath       string `json:"project_path"`
	GroupPath         string `json:"group_path"`                    // e.g., "projects/devops"
	Order             int    `json:"order"`                         // Position within group (for reorder persistence)
	ParentSessionID   string `json:"parent_session_id,omitempty"`   // Links to parent session (makes this a sub-session)
	ParentProjectPath string `json:"parent_project_path,omitempty"` // Parent's project path (for --add-dir access)

	// Git worktree support
	WorktreePath     string `json:"worktree_path,omitempty"`      // Path to worktree (if session is in worktree)
	WorktreeRepoRoot string `json:"worktree_repo_root,omitempty"` // Original repo root
	WorktreeBranch   string `json:"worktree_branch,omitempty"`    // Branch name in worktree

	Command        string    `json:"command"`
	Wrapper        string    `json:"wrapper,omitempty"` // Optional wrapper command with {command} placeholder
	Tool           string    `json:"tool"`
	Status         Status    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	LastAccessedAt time.Time `json:"last_accessed_at,omitempty"` // When user last attached

	// Claude Code integration
	ClaudeSessionID  string    `json:"claude_session_id,omitempty"`
	ClaudeDetectedAt time.Time `json:"claude_detected_at,omitempty"`

	// Gemini CLI integration
	GeminiSessionID  string                  `json:"gemini_session_id,omitempty"`
	GeminiDetectedAt time.Time               `json:"gemini_detected_at,omitempty"`
	GeminiYoloMode   *bool                   `json:"gemini_yolo_mode,omitempty"` // Per-session override (nil = use global config)
	GeminiModel      string                  `json:"gemini_model,omitempty"`     // Active model for this session
	GeminiAnalytics  *GeminiSessionAnalytics `json:"gemini_analytics,omitempty"` // Per-session analytics

	// OpenCode CLI integration
	OpenCodeSessionID  string    `json:"opencode_session_id,omitempty"`
	OpenCodeDetectedAt time.Time `json:"opencode_detected_at,omitempty"`
	OpenCodeStartedAt  int64     `json:"-"` // Unix millis when we started OpenCode (for session matching, not persisted)

	// Codex CLI integration
	CodexSessionID  string    `json:"codex_session_id,omitempty"`
	CodexDetectedAt time.Time `json:"codex_detected_at,omitempty"`
	CodexStartedAt  int64     `json:"-"` // Unix millis when we started Codex (for session matching, not persisted)
	lastCodexScanAt time.Time // Rate-limits expensive ~/.codex/sessions scans

	// Latest user input for context (extracted from session files)
	LatestPrompt      string    `json:"latest_prompt,omitempty"`
	lastPromptModTime time.Time // mtime cache for updateGeminiLatestPrompt (not serialized)

	// JSONL tail-read cache: skip re-reading if file hasn't grown
	lastJSONLSize int64
	lastJSONLPath string
	cachedPrompt  string

	// MCP tracking - which MCPs were loaded when session started/restarted
	// Used to detect pending MCPs (added after session start) and stale MCPs (removed but still running)
	LoadedMCPNames []string `json:"loaded_mcp_names,omitempty"`

	// ToolOptions stores tool-specific launch options (Claude, Codex, Gemini, etc.)
	// JSON structure: {"tool": "claude", "options": {...}}
	ToolOptionsJSON json.RawMessage `json:"tool_options,omitempty"`

	tmuxSession *tmux.Session // Internal tmux session

	// Hook-based status detection (set by StatusFileWatcher from Claude Code hooks)
	hookStatus     string    // running, idle, waiting, dead (empty = no hook data)
	hookSessionID  string    // Session ID from hook payload
	hookLastUpdate time.Time // When hook status was last received

	// mu protects fields written by backgroundStatusUpdate and read by the TUI goroutine.
	// Use GetStatus()/SetStatus() and GetTool()/SetTool() for thread-safe access.
	// UpdateStatus() acquires the write lock internally.
	mu sync.RWMutex

	// lastErrorCheck tracks when we last confirmed the session doesn't exist
	// Used to skip expensive Exists() checks for ghost sessions (sessions in JSON but not in tmux)
	// Not serialized - resets on load, but that's fine since we'll recheck on first poll
	lastErrorCheck time.Time

	// Tiered polling: skip expensive checks for idle sessions with no activity
	lastIdleCheck     time.Time // When we last did a full check for an idle session
	lastKnownActivity int64     // Last window_activity timestamp seen

	// lastStartTime tracks when Start() was called
	// Used to provide grace period for tmux session creation (prevents error flash)
	// Not serialized - only relevant for current TUI session
	lastStartTime time.Time

	// SkipMCPRegenerate skips .mcp.json regeneration on next Restart()
	// Set by MCP dialog Apply() to avoid race condition where Apply writes
	// config then Restart immediately overwrites it with different pool state
	SkipMCPRegenerate bool `json:"-"` // Don't persist, transient flag
}

// GetStatusThreadSafe returns the session status with read-lock protection.
// Use this when reading Status from a goroutine concurrent with backgroundStatusUpdate.
func (inst *Instance) GetStatusThreadSafe() Status {
	inst.mu.RLock()
	s := inst.Status
	inst.mu.RUnlock()
	return s
}

// SetStatusThreadSafe sets the session status with write-lock protection.
func (inst *Instance) SetStatusThreadSafe(s Status) {
	inst.mu.Lock()
	inst.Status = s
	inst.mu.Unlock()
}

// GetToolThreadSafe returns the tool name with read-lock protection.
func (inst *Instance) GetToolThreadSafe() string {
	inst.mu.RLock()
	t := inst.Tool
	inst.mu.RUnlock()
	return t
}

// SetToolThreadSafe sets the tool name with write-lock protection.
func (inst *Instance) SetToolThreadSafe(t string) {
	inst.mu.Lock()
	inst.Tool = t
	inst.mu.Unlock()
}

// MarkAccessed updates the LastAccessedAt timestamp to now
func (inst *Instance) MarkAccessed() {
	inst.LastAccessedAt = time.Now()
}

// GetLastActivityTime returns when the session was last active (content changed)
// Returns CreatedAt if no activity has been tracked yet
func (inst *Instance) GetLastActivityTime() time.Time {
	if inst.tmuxSession != nil {
		activityTime := inst.tmuxSession.GetLastActivityTime()
		if !activityTime.IsZero() {
			return activityTime
		}
	}
	// Fallback to CreatedAt
	return inst.CreatedAt
}

// GetWaitingSince returns when the session transitioned to waiting status
// Used for sorting notification bar (newest waiting sessions first)
func (inst *Instance) GetWaitingSince() time.Time {
	if inst.tmuxSession != nil {
		waitingSince := inst.tmuxSession.GetWaitingSince()
		if !waitingSince.IsZero() {
			return waitingSince
		}
	}
	// Fallback to CreatedAt if no waiting time tracked
	return inst.CreatedAt
}

// IsSubSession returns true if this session has a parent
func (inst *Instance) IsSubSession() bool {
	return inst.ParentSessionID != ""
}

// IsWorktree returns true if this session is running in a git worktree
func (inst *Instance) IsWorktree() bool {
	return inst.WorktreePath != ""
}

// SetParent sets the parent session ID
func (inst *Instance) SetParent(parentID string) {
	inst.ParentSessionID = parentID
}

// SetParentWithPath sets both parent session ID and parent's project path
// The project path is used to grant subagent access via --add-dir
func (inst *Instance) SetParentWithPath(parentID, parentProjectPath string) {
	inst.ParentSessionID = parentID
	inst.ParentProjectPath = parentProjectPath
}

// ClearParent removes the parent session link
func (inst *Instance) ClearParent() {
	inst.ParentSessionID = ""
	inst.ParentProjectPath = ""
}

// NewInstance creates a new session instance
func NewInstance(title, projectPath string) *Instance {
	id := generateID()
	tmuxSess := tmux.NewSession(title, projectPath)
	tmuxSess.InstanceID = id // Pass instance ID for activity hooks
	tmuxSess.SetInjectStatusLine(GetTmuxSettings().GetInjectStatusLine())

	return &Instance{
		ID:          id,
		Title:       title,
		ProjectPath: projectPath,
		GroupPath:   extractGroupPath(projectPath), // Auto-assign group from path
		Tool:        "shell",
		Status:      StatusIdle,
		CreatedAt:   time.Now(),
		tmuxSession: tmuxSess,
	}
}

// NewInstanceWithGroup creates a new session instance with explicit group
func NewInstanceWithGroup(title, projectPath, groupPath string) *Instance {
	inst := NewInstance(title, projectPath)
	inst.GroupPath = groupPath
	return inst
}

// NewInstanceWithTool creates a new session with tool-specific initialization
func NewInstanceWithTool(title, projectPath, tool string) *Instance {
	id := generateID()
	tmuxSess := tmux.NewSession(title, projectPath)
	tmuxSess.InstanceID = id // Pass instance ID for activity hooks
	tmuxSess.SetInjectStatusLine(GetTmuxSettings().GetInjectStatusLine())

	inst := &Instance{
		ID:          id,
		Title:       title,
		ProjectPath: projectPath,
		GroupPath:   extractGroupPath(projectPath),
		Tool:        tool,
		Status:      StatusIdle,
		CreatedAt:   time.Now(),
		tmuxSession: tmuxSess,
	}

	// Claude session ID will be detected from files Claude creates
	// No pre-assignment needed

	return inst
}

// NewInstanceWithGroupAndTool creates a new session with explicit group and tool
func NewInstanceWithGroupAndTool(title, projectPath, groupPath, tool string) *Instance {
	inst := NewInstanceWithTool(title, projectPath, tool)
	inst.GroupPath = groupPath
	return inst
}

// extractGroupPath extracts a group path from project path
// e.g., "/home/user/projects/devops" -> "projects"
func extractGroupPath(projectPath string) string {
	parts := strings.Split(projectPath, "/")
	// Find meaningful directory (skip Users, home, etc.)
	for i := len(parts) - 1; i >= 0; i-- {
		part := parts[i]
		if part != "" && part != "Users" && part != "home" && !strings.HasPrefix(part, ".") {
			// Return parent directory as group if we're at project level
			if i > 0 && i == len(parts)-1 {
				parent := parts[i-1]
				if parent != "" && parent != "Users" && parent != "home" && !strings.HasPrefix(parent, ".") {
					return parent
				}
			}
			return part
		}
	}
	return DefaultGroupName
}

// buildClaudeCommand builds the claude command with session capture
// For new sessions: captures session ID via print mode, stores in tmux env, then resumes
// This ensures we always know the session ID for fork/restart features
// Respects: CLAUDE_CONFIG_DIR, dangerous_mode from user config, and [shell].env_files
func (i *Instance) buildClaudeCommand(baseCommand string) string {
	envPrefix := i.buildEnvSourceCommand()
	cmd := i.buildClaudeCommandWithMessage(baseCommand, "")
	return envPrefix + cmd
}

// buildClaudeCommandWithMessage builds the command with optional initial message
// Respects ClaudeOptions from instance if set, otherwise falls back to config defaults
func (i *Instance) buildClaudeCommandWithMessage(baseCommand, message string) string {
	if i.Tool != "claude" {
		return baseCommand
	}

	// Get the configured Claude command (e.g., "claude", "cdw", "cdp")
	// If a custom command is set, we skip CLAUDE_CONFIG_DIR prefix since the alias handles it
	claudeCmd := GetClaudeCommand()
	hasCustomCommand := claudeCmd != "claude"

	// Check if CLAUDE_CONFIG_DIR is explicitly configured (env var or config.toml)
	// If NOT explicit, we don't set it in the command - let the shell's environment handle it.
	// This is critical for WSL and other environments where users have CLAUDE_CONFIG_DIR
	// set in their .bashrc/.zshrc - we should NOT override that with a default path.
	// Also skip if using a custom command (alias handles config dir)
	configDirPrefix := ""
	if !hasCustomCommand && IsClaudeConfigDirExplicit() {
		configDir := GetClaudeConfigDir()
		configDirPrefix = fmt.Sprintf("CLAUDE_CONFIG_DIR=%s ", configDir)
	}

	// AGENTDECK_INSTANCE_ID is set as an inline env var so Claude's hook subprocesses
	// can identify which agent-deck session they belong to.
	instanceIDPrefix := fmt.Sprintf("AGENTDECK_INSTANCE_ID=%s ", i.ID)
	configDirPrefix = instanceIDPrefix + configDirPrefix

	// Get options - either from instance or create defaults from config
	opts := i.GetClaudeOptions()
	if opts == nil {
		// Fall back to config defaults
		userConfig, _ := LoadUserConfig()
		opts = NewClaudeOptions(userConfig)
	}

	// If baseCommand is just "claude", build the appropriate command
	if baseCommand == "claude" {
		// Build extra flags string from options (includes --add-dir if ParentProjectPath set)
		extraFlags := i.buildClaudeExtraFlags(opts)

		// Handle different session modes
		switch opts.SessionMode {
		case "continue":
			// Simple -c mode: continue last session
			return fmt.Sprintf(`%s%s -c%s`, configDirPrefix, claudeCmd, extraFlags)

		case "resume":
			// Resume specific session by ID
			if opts.ResumeSessionID != "" {
				// Check if session has actual conversation data
				if sessionHasConversationData(opts.ResumeSessionID, i.ProjectPath) {
					// Session has conversation history - use normal --resume
					return fmt.Sprintf(`%s%s --resume %s%s`,
						configDirPrefix, claudeCmd, opts.ResumeSessionID, extraFlags)
				}
				// Session was never interacted with - use --session-id with same UUID
				// This handles the case where session was started but no message was sent
				bashExportPrefix := fmt.Sprintf("export AGENTDECK_INSTANCE_ID=%s; ", i.ID)
				if IsClaudeConfigDirExplicit() {
					configDir := GetClaudeConfigDir()
					bashExportPrefix += fmt.Sprintf("export CLAUDE_CONFIG_DIR=%s; ", configDir)
				}
				return fmt.Sprintf(
					`tmux set-environment CLAUDE_SESSION_ID "%s"; %sclaude --session-id "%s"%s`,
					opts.ResumeSessionID, bashExportPrefix, opts.ResumeSessionID, extraFlags)
			}
			// No session ID provided - use -r flag for interactive picker
			return fmt.Sprintf(`%s%s -r%s`, configDirPrefix, claudeCmd, extraFlags)
		}

		// Default: new session with capture-resume pattern
		// 1. Starts Claude in print mode to get session ID
		// 2. Stores session ID in tmux environment (if capture succeeded)
		// 3. Resumes that session interactively
		// Fallback ensures Claude starts (without fork/restart support) rather than failing completely
		//
		// IMPORTANT: For capture-resume commands (which contain $(...) syntax), we MUST use
		// "claude" binary + CLAUDE_CONFIG_DIR, NOT a custom command alias like "cdw".
		// Reason: Commands with $(...) get wrapped in `bash -c` for fish compatibility (#47),
		// and shell aliases are not available in non-interactive bash shells.
		//
		bashExportPrefix := fmt.Sprintf("export AGENTDECK_INSTANCE_ID=%s; ", i.ID)
		if IsClaudeConfigDirExplicit() {
			configDir := GetClaudeConfigDir()
			bashExportPrefix += fmt.Sprintf("export CLAUDE_CONFIG_DIR=%s; ", configDir)
		}

		var baseCmd string
		// Pre-generate UUID and use --session-id flag (instant, no API call)
		// Note: --session-id works for new sessions as of Claude CLI 2.1.x
		baseCmd = fmt.Sprintf(
			`session_id=$(uuidgen | tr '[:upper:]' '[:lower:]'); `+
				`tmux set-environment CLAUDE_SESSION_ID "$session_id"; `+
				`%sclaude --session-id "$session_id"%s`,
			bashExportPrefix, extraFlags)

		// If message provided, append wait-and-send logic
		if message != "" {
			// Escape single quotes in message for bash
			escapedMsg := strings.ReplaceAll(message, "'", "'\"'\"'")

			// Pre-generate UUID, then wait-and-send message in background
			baseCmd = fmt.Sprintf(
				`session_id=$(uuidgen | tr '[:upper:]' '[:lower:]'); `+
					`tmux set-environment CLAUDE_SESSION_ID "$session_id"; `+
					`(sleep 2; SESSION_NAME=$(tmux display-message -p '#S'); `+
					`while ! tmux capture-pane -p -t "$SESSION_NAME" | tail -5 | grep -qE "^>"; do sleep 0.2; done; `+
					`tmux send-keys -l -t "$SESSION_NAME" -- '%s' \\; send-keys -t "$SESSION_NAME" Enter) & `+
					`%sclaude --session-id "$session_id"%s`,
				escapedMsg,
				bashExportPrefix, extraFlags)
		}

		return baseCmd
	}

	// For custom commands (e.g., fork commands), return as-is
	return baseCommand
}

// buildClaudeExtraFlags builds extra command-line flags string from ClaudeOptions
// Also handles instance-level flags like --add-dir for subagent access
func (i *Instance) buildClaudeExtraFlags(opts *ClaudeOptions) string {
	var flags []string

	// Instance-level flags (not from ClaudeOptions)
	// --add-dir: Grant subagent access to parent's project directory (for worktrees, etc.)
	if i.ParentProjectPath != "" {
		flags = append(flags, fmt.Sprintf("--add-dir %s", i.ParentProjectPath))
	}

	// Options-level flags
	if opts != nil {
		if opts.SkipPermissions {
			flags = append(flags, "--dangerously-skip-permissions")
		} else if opts.AllowSkipPermissions {
			flags = append(flags, "--allow-dangerously-skip-permissions")
		}
		if opts.UseChrome {
			flags = append(flags, "--chrome")
		}
		if opts.UseTeammateMode {
			flags = append(flags, "--teammate-mode tmux")
		}
	}

	if len(flags) == 0 {
		return ""
	}
	return " " + strings.Join(flags, " ")
}

// buildGeminiCommand builds the gemini command with session capture
// For new sessions: captures session ID via stream-json, stores in tmux env, then resumes
// For sessions with known ID: uses simple resume
// This ensures we always know the session ID for restart features
// VERIFIED: gemini --output-format stream-json provides immediate session ID in first message
// Also sources .env files from [shell].env_files and [gemini].env_file
func (i *Instance) buildGeminiCommand(baseCommand string) string {
	if i.Tool != "gemini" {
		return baseCommand
	}

	envPrefix := i.buildEnvSourceCommand()

	// Determine if YOLO mode is enabled (per-session overrides global config)
	yoloMode := false
	if i.GeminiYoloMode != nil {
		yoloMode = *i.GeminiYoloMode
	} else {
		// Check global config
		userConfig, _ := LoadUserConfig()
		if userConfig != nil {
			yoloMode = userConfig.Gemini.YoloMode
		}
	}

	yoloFlag := ""
	yoloEnv := "false"
	if yoloMode {
		yoloFlag = " --yolo"
		yoloEnv = "true"
	}

	// Determine model flag
	modelFlag := ""
	if i.GeminiModel != "" {
		modelFlag = " --model " + i.GeminiModel
	} else if i.GeminiSessionID == "" {
		// Only apply default model for NEW sessions (not resumes)
		userConfig, _ := LoadUserConfig()
		if userConfig != nil && userConfig.Gemini.DefaultModel != "" {
			modelFlag = " --model " + userConfig.Gemini.DefaultModel
		}
	}

	// If baseCommand is just "gemini", handle specially
	if baseCommand == "gemini" {
		// If we already have a session ID, use simple resume
		if i.GeminiSessionID != "" {
			return envPrefix + fmt.Sprintf("tmux set-environment GEMINI_YOLO_MODE %s; tmux set-environment GEMINI_SESSION_ID %s; gemini --resume %s%s%s", yoloEnv, i.GeminiSessionID, i.GeminiSessionID, yoloFlag, modelFlag)
		}

		// Start Gemini fresh - session ID will be captured when user interacts
		// The previous capture-resume approach (gemini --output-format json ".") would hang
		// because Gemini processes the "." prompt which takes too long
		return envPrefix + fmt.Sprintf(`tmux set-environment GEMINI_YOLO_MODE %s; gemini%s%s`, yoloEnv, yoloFlag, modelFlag)
	}

	// For custom commands (e.g., resume commands), return as-is
	return envPrefix + baseCommand
}

// buildOpenCodeCommand builds the command for OpenCode CLI
// OpenCode stores sessions in ~/.local/share/opencode/storage/session/
// Session IDs are in format: ses_XXXXX
// Resume: opencode -s <session-id> or opencode --session <session-id>
// Continue last: opencode -c or opencode --continue
// Model: opencode -m provider/model
// Agent: opencode --agent name
// Also sources .env files from [shell].env_files
func (i *Instance) buildOpenCodeCommand(baseCommand string) string {
	if i.Tool != "opencode" {
		return baseCommand
	}

	envPrefix := i.buildEnvSourceCommand()

	// If baseCommand is just "opencode", handle specially
	if baseCommand == "opencode" {
		extraFlags := i.buildOpenCodeExtraFlags()

		// If we already have a session ID, use resume with -s flag
		if i.OpenCodeSessionID != "" {
			return envPrefix + fmt.Sprintf("tmux set-environment OPENCODE_SESSION_ID %s; opencode -s %s%s",
				i.OpenCodeSessionID, i.OpenCodeSessionID, extraFlags)
		}

		// Start OpenCode fresh - session ID will be captured async after startup
		return envPrefix + "opencode" + extraFlags
	}

	// For custom commands (e.g., fork commands), return as-is
	return envPrefix + baseCommand
}

// buildOpenCodeExtraFlags returns extra CLI flags from OpenCodeOptions (model, agent).
// Returns a string with leading space, or empty string if no flags.
func (i *Instance) buildOpenCodeExtraFlags() string {
	opts := i.GetOpenCodeOptions()
	if opts == nil {
		// Fall back to global config defaults
		if config, err := LoadUserConfig(); err == nil && config != nil {
			opts = NewOpenCodeOptions(config)
		}
	}
	if opts == nil {
		return ""
	}

	var flags string
	if opts.Model != "" {
		flags += " -m " + opts.Model
	}
	if opts.Agent != "" {
		flags += " --agent " + opts.Agent
	}
	return flags
}

// DetectOpenCodeSession is the public wrapper for async OpenCode session detection
// Call this for restored sessions that don't have a session ID yet
func (i *Instance) DetectOpenCodeSession() {
	i.detectOpenCodeSessionAsync()
}

// buildCodexCommand builds the command for OpenAI Codex CLI
// resolveCodexYoloFlag returns " --yolo" if yolo mode is enabled (per-session override > global config), or "".
func (i *Instance) resolveCodexYoloFlag() string {
	opts := i.GetCodexOptions()
	if opts != nil && opts.YoloMode != nil {
		if *opts.YoloMode {
			return " --yolo"
		}
		return ""
	}
	// Fallback to global config
	if config, err := LoadUserConfig(); err == nil && config != nil {
		if config.Codex.YoloMode {
			return " --yolo"
		}
	}
	return ""
}

// Codex stores sessions in ~/.codex/sessions/YYYY/MM/DD/*.jsonl
// Resume: codex resume <session-id> or codex resume --last
// Also sources .env files from [shell].env_files
func (i *Instance) buildCodexCommand(baseCommand string) string {
	if i.Tool != "codex" {
		return baseCommand
	}

	envPrefix := i.buildEnvSourceCommand()
	agentdeckEnvPrefix := fmt.Sprintf("AGENTDECK_INSTANCE_ID=%s AGENTDECK_TITLE=%q AGENTDECK_TOOL=%s ",
		i.ID, i.Title, i.Tool)
	envPrefix += agentdeckEnvPrefix

	yoloFlag := i.resolveCodexYoloFlag()

	// If baseCommand is just "codex", handle specially
	if baseCommand == "codex" {
		// If we already have a session ID, use resume
		if i.CodexSessionID != "" {
			return envPrefix + fmt.Sprintf("tmux set-environment CODEX_SESSION_ID %s; codex%s resume %s",
				i.CodexSessionID, yoloFlag, i.CodexSessionID)
		}

		// Start Codex fresh - session ID will be captured async after startup
		return envPrefix + "codex" + yoloFlag
	}

	// For custom commands (e.g., resume commands), preserve env propagation.
	return envPrefix + baseCommand
}

// detectOpenCodeSessionAsync detects the OpenCode session ID after startup
// OpenCode generates session IDs internally (format: ses_XXXXX)
// We query "opencode session list --format json" and match by project directory,
// picking the most recently updated session (since OpenCode auto-resumes the last session)
func (i *Instance) detectOpenCodeSessionAsync() {
	time.Sleep(1 * time.Second)

	// Phase 1: Quick detection for existing sessions (5 attempts over ~15s)
	quickDelays := []time.Duration{0, 2 * time.Second, 3 * time.Second, 4 * time.Second, 5 * time.Second}

	for attempt, delay := range quickDelays {
		if delay > 0 {
			time.Sleep(delay)
		}

		if sessionID := i.queryOpenCodeSession(); sessionID != "" {
			i.setOpenCodeSession(sessionID)
			sessionLog.Debug("opencode_session_detected", slog.String("session_id", sessionID), slog.String("phase", "quick"), slog.Int("attempt", attempt+1))
			return
		}

		sessionLog.Debug("opencode_session_not_found", slog.Int("attempt", attempt+1), slog.Int("total", len(quickDelays)))
	}

	// Phase 2: Long-running background watcher for new sessions
	// OpenCode only persists new sessions after significant user activity
	go i.watchForOpenCodeSession()
}

// watchForOpenCodeSession polls for session creation over an extended period.
// New sessions may take minutes to be persisted by OpenCode.
func (i *Instance) watchForOpenCodeSession() {
	const (
		pollInterval = 10 * time.Second
		maxDuration  = 5 * time.Minute
	)

	deadline := time.Now().Add(maxDuration)
	attempt := 0

	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)
		attempt++

		if i.OpenCodeSessionID != "" {
			sessionLog.Debug("opencode_watcher_already_set")
			return
		}

		if sessionID := i.queryOpenCodeSession(); sessionID != "" {
			i.setOpenCodeSession(sessionID)
			sessionLog.Debug("opencode_watcher_detected", slog.String("session_id", sessionID), slog.Int("attempt", attempt))
			return
		}

		sessionLog.Debug("opencode_watcher_not_found", slog.Int("attempt", attempt))
	}

	sessionLog.Debug("opencode_watcher_timeout", slog.Duration("max_duration", maxDuration))
}

// setOpenCodeSession sets the session ID and stores it in tmux environment.
func (i *Instance) setOpenCodeSession(sessionID string) {
	i.OpenCodeSessionID = sessionID
	i.OpenCodeDetectedAt = time.Now()

	if i.tmuxSession != nil {
		if err := i.tmuxSession.SetEnvironment("OPENCODE_SESSION_ID", sessionID); err != nil {
			sessionLog.Warn("opencode_set_env_failed", slog.String("error", err.Error()))
		}
	}
}

// queryOpenCodeSession queries OpenCode CLI for session matching our project directory
// OpenCode automatically resumes the most recent session for a directory, so we
// simply find the most recently updated session matching our project path.
func (i *Instance) queryOpenCodeSession() string {
	// Run: opencode session list --format json
	cmd := exec.Command("opencode", "session", "list", "--format", "json")
	cmd.Dir = i.ProjectPath

	sessionLog.Debug("opencode_query_sessions", slog.String("dir", i.ProjectPath))

	output, err := cmd.Output()
	if err != nil {
		sessionLog.Debug("opencode_query_failed", slog.String("error", err.Error()))
		return ""
	}

	sessionLog.Debug("opencode_session_data_size", slog.Int("bytes", len(output)))

	// Parse JSON response
	// Expected format: array of session objects with id, directory, created, updated fields
	var sessions []struct {
		ID        string `json:"id"`
		Directory string `json:"directory"`
		Path      string `json:"path"`    // Some versions use path instead of directory
		Created   int64  `json:"created"` // Unix timestamp (milliseconds)
		Updated   int64  `json:"updated"` // Unix timestamp (milliseconds) - when last active
	}

	if err := json.Unmarshal(output, &sessions); err != nil {
		sessionLog.Debug("opencode_parse_failed", slog.String("error", err.Error()))
		return ""
	}

	sessionLog.Debug("opencode_parsed_sessions", slog.Int("count", len(sessions)))

	// Find the most recently updated session matching our project path
	// OpenCode auto-resumes the most recent session when you run `opencode` in a directory,
	// so we track that same session (no startTime check needed)
	projectPath := i.ProjectPath

	var bestMatch string
	var bestMatchTime int64

	for _, sess := range sessions {
		// Check directory match (normalize paths)
		sessDir := sess.Directory
		if sessDir == "" {
			sessDir = sess.Path
		}

		normalizedSessDir := normalizePath(sessDir)
		normalizedProjectPath := normalizePath(projectPath)

		sessionLog.Debug("opencode_session_compare", slog.String("session_id", sess.ID), slog.String("sess_dir", sessDir), slog.String("project_path", projectPath), slog.Int64("created", sess.Created), slog.Int64("updated", sess.Updated))

		// Normalize both paths for comparison
		if sessDir == "" || normalizedSessDir != normalizedProjectPath {
			sessionLog.Debug("opencode_session_dir_mismatch", slog.String("session_id", sess.ID))
			continue
		}

		// Pick the most recently updated session for this directory
		updatedAt := sess.Updated
		if updatedAt == 0 {
			updatedAt = sess.Created // Fallback to created if updated not available
		}

		sessionLog.Debug("opencode_session_dir_match", slog.String("session_id", sess.ID), slog.Int64("updated", updatedAt))

		if bestMatch == "" || updatedAt > bestMatchTime {
			bestMatch = sess.ID
			bestMatchTime = updatedAt
		}
	}

	sessionLog.Debug("opencode_best_match", slog.String("session_id", bestMatch), slog.Int64("updated", bestMatchTime))
	return bestMatch
}

// normalizePath normalizes a file path for comparison
func normalizePath(p string) string {
	// Expand home directory
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			p = strings.Replace(p, "~", home, 1)
		}
	}

	// Clean the path
	p = filepath.Clean(p)

	// Resolve symlinks if possible
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}

	return p
}

// DetectCodexSession is the public wrapper for async Codex session detection
// Call this for restored sessions that don't have a session ID yet
func (i *Instance) DetectCodexSession() {
	i.detectCodexSessionAsync()
}

// detectCodexSessionAsync detects the Codex session ID after startup
// Codex stores sessions in ~/.codex/sessions/YYYY/MM/DD/*.jsonl
// Session ID is a UUID that can be extracted from the filename
// Since Codex has no "session list" command, we scan the filesystem
func (i *Instance) detectCodexSessionAsync() {
	// Brief wait for Codex to initialize
	time.Sleep(1 * time.Second)

	// Try up to 3 times with short delays
	delays := []time.Duration{0, 1 * time.Second, 2 * time.Second}

	for attempt, delay := range delays {
		if delay > 0 {
			time.Sleep(delay)
		}

		sessionID := i.queryCodexSession(i.collectOtherCodexSessionIDs(), true)
		if sessionID != "" {
			i.CodexSessionID = sessionID
			i.CodexDetectedAt = time.Now()

			// Store in tmux environment for restart
			if i.tmuxSession != nil {
				if err := i.tmuxSession.SetEnvironment("CODEX_SESSION_ID", sessionID); err != nil {
					sessionLog.Warn("codex_set_env_failed", slog.String("error", err.Error()))
				}
			}

			sessionLog.Debug("codex_session_detected", slog.String("session_id", sessionID), slog.Int("attempt", attempt+1))
			return
		}

		sessionLog.Debug("codex_session_not_found", slog.Int("attempt", attempt+1), slog.Int("total", len(delays)))
	}

	sessionLog.Warn("codex_detection_failed", slog.Int("attempts", len(delays)))
}

func getCodexHomeDir() string {
	if codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME")); codexHome != "" {
		return codexHome
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".codex")
	}
	return filepath.Join(home, ".codex")
}

// queryCodexSession scans Codex sessions and returns the best candidate.
// Selection strategy:
//  1. Prefer sessions whose JSONL metadata matches this instance's project path.
//  2. Optionally allow unscoped fallback (no cwd metadata) for initial bootstrap.
func (i *Instance) queryCodexSession(excludeIDs map[string]bool, allowUnscoped bool) string {
	sessionsDir := filepath.Join(getCodexHomeDir(), "sessions")
	if _, err := os.Stat(sessionsDir); os.IsNotExist(err) {
		return ""
	}

	uuidPattern := regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

	var bestScopedID string
	var bestScopedTime time.Time
	var bestUnscopedID string
	var bestUnscopedTime time.Time

	normalizedProjectPath := normalizePath(i.ProjectPath)

	err := filepath.WalkDir(sessionsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // Skip errors
		}

		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}

		sessionID := uuidPattern.FindString(d.Name())
		if sessionID == "" {
			return nil
		}
		if excludeIDs != nil && excludeIDs[sessionID] {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		// Only consider sessions created after we started this instance.
		if i.CodexStartedAt > 0 {
			startTime := time.UnixMilli(i.CodexStartedAt)
			if info.ModTime().Before(startTime) {
				return nil
			}
		}

		matchesProject, hasProjectMetadata := codexSessionMatchesProject(path, normalizedProjectPath)
		if matchesProject {
			if bestScopedID == "" || info.ModTime().After(bestScopedTime) {
				bestScopedID = sessionID
				bestScopedTime = info.ModTime()
			}
			return nil
		}

		// Use unscoped records only when bootstrapping and metadata is unavailable.
		if allowUnscoped && !hasProjectMetadata {
			if bestUnscopedID == "" || info.ModTime().After(bestUnscopedTime) {
				bestUnscopedID = sessionID
				bestUnscopedTime = info.ModTime()
			}
		}

		return nil
	})
	if err != nil {
		sessionLog.Debug("codex_scan_error", slog.String("error", err.Error()))
	}

	if bestScopedID != "" {
		return bestScopedID
	}
	if allowUnscoped {
		return bestUnscopedID
	}
	return ""
}

// codexSessionMatchesProject checks whether a Codex session file belongs to the
// current project by inspecting JSONL metadata fields (cwd/workdir/path).
// Returns:
//   - match=true if project path matches
//   - known=true if any project metadata field was found
func codexSessionMatchesProject(sessionFilePath, normalizedProjectPath string) (match bool, known bool) {
	if normalizedProjectPath == "" {
		return false, false
	}

	file, err := os.Open(sessionFilePath)
	if err != nil {
		return false, false
	}
	defer file.Close()

	const maxLines = 256

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	lineCount := 0
	foundMetadata := false

	for scanner.Scan() {
		lineCount++
		cwd := extractCodexCWDFromJSONLine(scanner.Bytes())
		if cwd != "" {
			foundMetadata = true
			if normalizePath(cwd) == normalizedProjectPath {
				return true, true
			}
		}
		if lineCount >= maxLines {
			break
		}
	}

	return false, foundMetadata
}

// extractCodexCWDFromJSONLine extracts cwd-like project fields from one JSONL record.
func extractCodexCWDFromJSONLine(line []byte) string {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return ""
	}

	keys := []string{"cwd", "workdir", "working_dir", "directory", "path"}
	for _, key := range keys {
		if val := decodeJSONStringField(raw, key); val != "" {
			return val
		}
	}

	if payloadRaw, ok := raw["payload"]; ok && len(payloadRaw) > 0 {
		var payloadObj map[string]json.RawMessage
		if err := json.Unmarshal(payloadRaw, &payloadObj); err == nil {
			for _, key := range keys {
				if val := decodeJSONStringField(payloadObj, key); val != "" {
					return val
				}
			}
		}
	}
	if contextRaw, ok := raw["context"]; ok && len(contextRaw) > 0 {
		var contextObj map[string]json.RawMessage
		if err := json.Unmarshal(contextRaw, &contextObj); err == nil {
			for _, key := range keys {
				if val := decodeJSONStringField(contextObj, key); val != "" {
					return val
				}
			}
		}
	}

	return ""
}

func decodeJSONStringField(raw map[string]json.RawMessage, key string) string {
	value, ok := raw[key]
	if !ok || len(value) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(value, &s); err != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

// collectOtherCodexSessionIDs enumerates other managed tmux sessions and returns
// the CODEX_SESSION_ID values they currently own.
func (i *Instance) collectOtherCodexSessionIDs() map[string]bool {
	exclude := make(map[string]bool)

	tmuxSessions, err := tmux.ListAgentDeckSessions()
	if err != nil {
		return exclude
	}

	myTmuxName := ""
	if i.tmuxSession != nil {
		myTmuxName = i.tmuxSession.Name
	}

	for _, sessName := range tmuxSessions {
		if sessName == myTmuxName {
			continue
		}
		other := &tmux.Session{Name: sessName}
		if id, err := other.GetEnvironment("CODEX_SESSION_ID"); err == nil && id != "" {
			exclude[id] = true
		}
	}

	return exclude
}

// shouldScanCodexSession returns whether we should run an expensive filesystem
// scan for Codex session rotation right now.
func (i *Instance) shouldScanCodexSession(allowUnscoped bool) bool {
	interval := codexRotationScanInterval
	if allowUnscoped {
		interval = codexBootstrapScanInterval
	}

	if !i.lastCodexScanAt.IsZero() && time.Since(i.lastCodexScanAt) < interval {
		return false
	}

	i.lastCodexScanAt = time.Now()
	return true
}

// UpdateCodexSession updates the Codex session ID.
// Primary source: tmux environment.
// Fallback: project-aware filesystem scan.
func (i *Instance) UpdateCodexSession(excludeIDs map[string]bool) {
	if i.Tool != "codex" {
		return
	}

	envSessionID := ""

	// 1. Try to read from tmux environment first (authoritative if set)
	if i.tmuxSession != nil {
		if sessionID, err := i.tmuxSession.GetEnvironment("CODEX_SESSION_ID"); err == nil && sessionID != "" {
			envSessionID = sessionID
			if i.CodexSessionID != sessionID {
				i.CodexSessionID = sessionID
			}
			i.CodexDetectedAt = time.Now()
		}
	}

	// 2. Detect same-project session rotation (e.g. /new) from disk.
	// Only allow unscoped fallback when we don't have a known session ID yet.
	allowUnscoped := envSessionID == "" && i.CodexSessionID == "" && i.CodexStartedAt > 0
	if !i.shouldScanCodexSession(allowUnscoped) {
		return
	}

	if sessionID := i.queryCodexSession(excludeIDs, allowUnscoped); sessionID != "" {
		changed := sessionID != i.CodexSessionID
		if sessionID != i.CodexSessionID {
			sessionLog.Debug("codex_session_update", slog.String("old_id", i.CodexSessionID), slog.String("new_id", sessionID))
		}
		i.CodexSessionID = sessionID
		i.CodexDetectedAt = time.Now()

		// Sync back to tmux environment for future restarts
		// Skip redundant writes when env already matches: each write is a tmux subprocess.
		if i.tmuxSession != nil && i.tmuxSession.Exists() && (changed || envSessionID == "") {
			_ = i.tmuxSession.SetEnvironment("CODEX_SESSION_ID", i.CodexSessionID)
		}
	}
}

// buildGenericCommand builds commands for custom tools defined in [tools.*]
// If the tool has session resume config, builds capture-resume command similar to Claude/Gemini
// Otherwise returns the base command as-is
// Also sources .env files from [shell].env_files and [tools.X].env_file
//
// Config fields used:
//   - resume_flag: CLI flag to resume (e.g., "--resume")
//   - session_id_env: tmux env var name (e.g., "VIBE_SESSION_ID")
//   - session_id_json_path: jq path to extract ID (e.g., ".session_id")
//   - output_format_flag: flag to get JSON output (e.g., "--output-format json")
//   - dangerous_flag: flag to skip confirmations (e.g., "--auto-approve")
//   - dangerous_mode: whether to enable dangerous flag by default
//   - env_file: .env file to source for this tool
func (i *Instance) buildGenericCommand(baseCommand string) string {
	envPrefix := i.buildEnvSourceCommand()

	toolDef := GetToolDef(i.Tool)
	if toolDef == nil {
		return envPrefix + baseCommand // No custom config, return with env prefix
	}

	// Check if tool supports session resume (needs both resume_flag and session_id_env)
	if toolDef.ResumeFlag == "" || toolDef.SessionIDEnv == "" {
		// No session resume support, just add dangerous flag if configured
		if toolDef.DangerousMode && toolDef.DangerousFlag != "" {
			return envPrefix + fmt.Sprintf("%s %s", baseCommand, toolDef.DangerousFlag)
		}
		return envPrefix + baseCommand
	}

	// Get existing session ID from tmux environment (for restart/resume)
	existingSessionID := ""
	if i.tmuxSession != nil {
		if sid, err := i.tmuxSession.GetEnvironment(toolDef.SessionIDEnv); err == nil && sid != "" {
			existingSessionID = sid
		}
	}

	// Build dangerous flag if enabled
	dangerousFlag := ""
	if toolDef.DangerousMode && toolDef.DangerousFlag != "" {
		dangerousFlag = " " + toolDef.DangerousFlag
	}

	// If we have an existing session ID, just resume
	if existingSessionID != "" {
		return envPrefix + fmt.Sprintf("tmux set-environment %s %s && %s %s %s%s",
			toolDef.SessionIDEnv, existingSessionID,
			baseCommand, toolDef.ResumeFlag, existingSessionID, dangerousFlag)
	}

	// No existing session ID - need to capture it on first run
	// This requires output_format_flag and session_id_json_path
	if toolDef.OutputFormatFlag == "" || toolDef.SessionIDJsonPath == "" {
		// Can't capture session ID, just start normally
		if dangerousFlag != "" {
			return envPrefix + baseCommand + dangerousFlag
		}
		return envPrefix + baseCommand
	}

	// Build capture-resume command similar to Claude/Gemini
	// Pattern:
	// 1. Run tool with minimal prompt to get session ID
	// 2. Extract ID using jq
	// 3. Store in tmux environment
	// 4. Resume that session
	// Fallback: If capture fails, start tool fresh
	return envPrefix + fmt.Sprintf(
		`session_id=$(%s %s "." 2>/dev/null | jq -r '%s' 2>/dev/null) || session_id=""; `+
			`if [ -n "$session_id" ] && [ "$session_id" != "null" ]; then `+
			`tmux set-environment %s "$session_id"; `+
			`%s %s "$session_id"%s; `+
			`else %s%s; fi`,
		baseCommand, toolDef.OutputFormatFlag, toolDef.SessionIDJsonPath,
		toolDef.SessionIDEnv,
		baseCommand, toolDef.ResumeFlag, dangerousFlag,
		baseCommand, dangerousFlag)
}

// GetGenericSessionID gets session ID from tmux environment for a custom tool
// Uses the session_id_env field from tool config
func (i *Instance) GetGenericSessionID() string {
	toolDef := GetToolDef(i.Tool)
	if toolDef == nil || toolDef.SessionIDEnv == "" {
		return ""
	}
	if i.tmuxSession == nil {
		return ""
	}
	sessionID, err := i.tmuxSession.GetEnvironment(toolDef.SessionIDEnv)
	if err != nil {
		return ""
	}
	return sessionID
}

// CanRestartGeneric returns true if a custom tool can be restarted with session resume
func (i *Instance) CanRestartGeneric() bool {
	toolDef := GetToolDef(i.Tool)
	if toolDef == nil {
		return false
	}
	// Can restart if we have resume support AND an existing session ID
	if toolDef.ResumeFlag == "" || toolDef.SessionIDEnv == "" {
		return false
	}
	return i.GetGenericSessionID() != ""
}

func (i *Instance) applyWrapper(command string) (string, error) {
	wrapper := i.Wrapper
	if wrapper == "" {
		if toolDef := GetToolDef(i.Tool); toolDef != nil {
			wrapper = toolDef.Wrapper
		}
	}
	if wrapper == "" {
		return command, nil
	}
	if strings.Contains(wrapper, wrapperPlaceholder) {
		return strings.ReplaceAll(wrapper, wrapperPlaceholder, command), nil
	}
	return wrapper, nil
}

// loadCustomPatternsFromConfig loads detection patterns from built-in defaults + config.toml
// overrides, and sets them on the tmux session for status detection and tool auto-detection.
// Works for ALL tools: built-in (claude, gemini, opencode, codex) and custom.
func (i *Instance) loadCustomPatternsFromConfig() {
	if i.tmuxSession == nil {
		return
	}

	// Merge built-in defaults with any user config overrides/extras
	raw := MergeToolPatterns(i.Tool)
	if raw != nil {
		resolved, err := tmux.CompilePatterns(raw)
		if err != nil {
			sessionLog.Warn("pattern_compile_error", slog.String("tool", i.Tool), slog.String("error", err.Error()))
		}
		if resolved != nil {
			i.tmuxSession.SetPatterns(resolved)
		}
	}

	// Keep detect patterns for DetectTool() (separate from busy/prompt detection)
	if toolDef := GetToolDef(i.Tool); toolDef != nil {
		i.tmuxSession.SetDetectPatterns(i.Tool, toolDef.DetectPatterns)
	}
}

// Start starts the session in tmux
func (i *Instance) Start() error {
	if i.tmuxSession == nil {
		return fmt.Errorf("tmux session not initialized")
	}

	// Build command based on tool type
	// Priority: built-in tools (claude, gemini, opencode, codex) → custom tools from config.toml → raw command
	var command string
	switch i.Tool {
	case "claude":
		command = i.buildClaudeCommand(i.Command)
	case "gemini":
		command = i.buildGeminiCommand(i.Command)
	case "opencode":
		command = i.buildOpenCodeCommand(i.Command)
		// Record start time for session ID detection (Unix millis)
		i.OpenCodeStartedAt = time.Now().UnixMilli()
	case "codex":
		command = i.buildCodexCommand(i.Command)
		// Record start time for session ID detection (Unix millis)
		i.CodexStartedAt = time.Now().UnixMilli()
	default:
		// Check if this is a custom tool with session resume config
		if toolDef := GetToolDef(i.Tool); toolDef != nil {
			command = i.buildGenericCommand(i.Command)
		} else {
			command = i.Command
		}
	}

	var err error
	command, err = i.applyWrapper(command)
	if err != nil {
		return err
	}

	// Load custom patterns for status detection
	i.loadCustomPatternsFromConfig()

	// Apply user tmux option overrides from config (e.g. allow-passthrough = "all")
	if tmuxCfg := GetTmuxSettings(); len(tmuxCfg.Options) > 0 {
		i.tmuxSession.OptionOverrides = tmuxCfg.Options
	}

	// Start the tmux session
	if err := i.tmuxSession.Start(command); err != nil {
		return fmt.Errorf("failed to start tmux session: %w", err)
	}

	// Set AGENTDECK_INSTANCE_ID for Claude hooks to identify this session
	// This enables real-time status updates via Stop/SessionStart hooks
	if err := i.tmuxSession.SetEnvironment("AGENTDECK_INSTANCE_ID", i.ID); err != nil {
		sessionLog.Warn("set_instance_id_failed", slog.String("error", err.Error()))
	}

	// Capture MCPs that are now loaded (for sync tracking)
	i.CaptureLoadedMCPs()

	// Record start time for grace period (prevents error flash during tmux startup)
	i.lastStartTime = time.Now()

	// New sessions start as STARTING - shows they're initializing
	// After 5s grace period, status will be properly detected from tmux
	if command != "" {
		i.Status = StatusStarting
	}

	// Start async session ID detection for OpenCode
	// This runs in background and captures the session ID once OpenCode creates it
	if i.Tool == "opencode" {
		go i.detectOpenCodeSessionAsync()
	}

	// Start async session ID detection for Codex
	// This runs in background and captures the session ID once Codex creates it
	if i.Tool == "codex" {
		go i.detectCodexSessionAsync()
	}

	return nil
}

// StartWithMessage starts the session and sends an initial message when ready
// The message is sent synchronously after detecting the agent's prompt
// This approach is more reliable than embedding send logic in the tmux command
// Works for Claude, Gemini, OpenCode, and other agents
func (i *Instance) StartWithMessage(message string) error {
	if i.tmuxSession == nil {
		return fmt.Errorf("tmux session not initialized")
	}

	// Start session normally (no embedded message logic)
	// Priority: built-in tools (claude, gemini, opencode, codex) → custom tools from config.toml → raw command
	var command string
	switch i.Tool {
	case "claude":
		command = i.buildClaudeCommand(i.Command)
	case "gemini":
		command = i.buildGeminiCommand(i.Command)
	case "opencode":
		command = i.buildOpenCodeCommand(i.Command)
		i.OpenCodeStartedAt = time.Now().UnixMilli()
	case "codex":
		command = i.buildCodexCommand(i.Command)
		i.CodexStartedAt = time.Now().UnixMilli()
	default:
		// Check if this is a custom tool with session resume config
		if toolDef := GetToolDef(i.Tool); toolDef != nil {
			command = i.buildGenericCommand(i.Command)
		} else {
			command = i.Command
		}
	}

	var err error
	command, err = i.applyWrapper(command)
	if err != nil {
		return err
	}

	// Load custom patterns for status detection
	i.loadCustomPatternsFromConfig()

	// Apply user tmux option overrides from config (e.g. allow-passthrough = "all")
	if tmuxCfg := GetTmuxSettings(); len(tmuxCfg.Options) > 0 {
		i.tmuxSession.OptionOverrides = tmuxCfg.Options
	}

	// Start the tmux session
	if err := i.tmuxSession.Start(command); err != nil {
		return fmt.Errorf("failed to start tmux session: %w", err)
	}

	// Set AGENTDECK_INSTANCE_ID for Claude hooks to identify this session
	// This enables real-time status updates via Stop/SessionStart hooks
	if err := i.tmuxSession.SetEnvironment("AGENTDECK_INSTANCE_ID", i.ID); err != nil {
		sessionLog.Warn("set_instance_id_failed", slog.String("error", err.Error()))
	}

	// Capture MCPs that are now loaded (for sync tracking)
	i.CaptureLoadedMCPs()

	// Record start time for grace period (prevents error flash during tmux startup)
	i.lastStartTime = time.Now()

	// New sessions start as STARTING
	i.Status = StatusStarting

	// Start async session ID detection for tools that persist IDs out-of-band.
	if i.Tool == "opencode" {
		go i.detectOpenCodeSessionAsync()
	}
	if i.Tool == "codex" {
		go i.detectCodexSessionAsync()
	}

	// Send message synchronously (CLI will wait)
	if message != "" {
		return i.sendMessageWhenReady(message)
	}

	return nil
}

// sendMessageWhenReady waits for the agent to be ready and sends the message
// Uses the existing status detection system which is robust and works for all tools
//
// The status flow for a new session:
//  1. Initial "waiting" (session just started, hash set)
//  2. "active" (content changing as agent loads)
//  3. "waiting" (content stable, agent ready for input)
//
// We wait for this full cycle: initial → active → waiting
// Exception: If Claude already finished processing "." from session capture,
// we may see "waiting" immediately - detect this by checking for input prompt
func (i *Instance) sendMessageWhenReady(message string) error {
	if i.tmuxSession == nil {
		return fmt.Errorf("tmux session not initialized")
	}

	// Track state transitions: we need to see "active" before accepting "waiting"
	// This ensures we don't send the message during initial startup (false "waiting")
	sawActive := false
	readyCount := 0    // Track consecutive waiting/idle states to detect already-ready sessions
	maxAttempts := 300 // 60 seconds max (300 * 200ms) - Claude with MCPs can take 40-60s

	for attempt := 0; attempt < maxAttempts; attempt++ {
		time.Sleep(200 * time.Millisecond)

		// Use the existing robust status detection
		status, err := i.tmuxSession.GetStatus()
		if err != nil {
			readyCount = 0 // Reset on error
			continue
		}

		if status == "active" {
			sawActive = true
			readyCount = 0
			continue
		}

		if status == "waiting" || status == "idle" {
			readyCount++
		} else {
			readyCount = 0
		}

		// Agent is ready when either:
		// 1. We've seen "active" (loading) and now see "waiting" (ready)
		// 2. We've seen waiting/idle 10+ times consecutively (already processed initial ".")
		//    This handles the race where Claude finishes before we start checking
		alreadyReady := readyCount >= 10 && attempt >= 15 // At least 3s elapsed
		if (sawActive && (status == "waiting" || status == "idle")) || alreadyReady {
			if i.Tool == "claude" {
				if content, captureErr := i.tmuxSession.CapturePaneFresh(); captureErr == nil && !hasCurrentComposerPrompt(content) {
					// Claude can report waiting before the interactive prompt is visible.
					// Keep polling until the prompt line is present.
					continue
				}
			}
			// Small delay to ensure UI is fully rendered
			time.Sleep(300 * time.Millisecond)

			// Send message atomically (text + Enter in single tmux invocation)
			if err := i.tmuxSession.SendKeysAndEnter(message); err != nil {
				return fmt.Errorf("failed to send message: %w", err)
			}

			// Verify the agent accepted Enter and began processing.
			// Strategy:
			// - If unsent prompt is visible, press Enter again immediately.
			// - Consider success only after sustained post-send activity ("active").
			// - If we never observe active and remain in waiting/idle, keep a
			//   periodic fallback Enter cadence instead of returning early.
			const verifyRetries = 50
			const verifyDelay = 300 * time.Millisecond
			const activeSuccessThreshold = 2
			const waitingAfterActiveThreshold = 2
			waitingNoMarkerChecks := 0
			activeChecks := 0
			sawActiveAfterSend := false

			for retry := 0; retry < verifyRetries; retry++ {
				time.Sleep(verifyDelay)

				unsentPromptDetected := false
				if content, captureErr := i.tmuxSession.CapturePaneFresh(); captureErr == nil {
					unsentPromptDetected = hasUnsentPastedPrompt(content) || hasUnsentComposerPrompt(content, message)
				}
				verifiedStatus, statusErr := i.tmuxSession.GetStatus()

				if unsentPromptDetected {
					waitingNoMarkerChecks = 0
					activeChecks = 0
					_ = i.tmuxSession.SendEnter()
					continue
				}

				if statusErr == nil && verifiedStatus == "active" {
					sawActiveAfterSend = true
					waitingNoMarkerChecks = 0
					activeChecks++
					if activeChecks >= activeSuccessThreshold {
						return nil
					}
					continue
				}
				activeChecks = 0

				if statusErr == nil && (verifiedStatus == "waiting" || verifiedStatus == "idle") {
					if sawActiveAfterSend {
						waitingNoMarkerChecks++
						if waitingNoMarkerChecks >= waitingAfterActiveThreshold {
							return nil
						}
					} else {
						waitingNoMarkerChecks = 0
						// We haven't observed any post-send activity yet.
						// Periodically nudge Enter while waiting to handle
						// late prompt-state races.
						if retry%3 == 2 {
							_ = i.tmuxSession.SendEnter()
						}
					}
					continue
				}

				waitingNoMarkerChecks = 0
				if retry < 2 {
					_ = i.tmuxSession.SendEnter()
				}
			}

			// Best effort: don't fail if verification remains inconclusive.
			return nil
		}
	}

	return fmt.Errorf("timeout waiting for agent to be ready")
}

// hasUnsentPastedPrompt detects Claude's composer marker for pasted text that
// has not been submitted yet.
func hasUnsentPastedPrompt(content string) bool {
	return strings.Contains(strings.ToLower(content), "[pasted text")
}

func normalizePromptText(s string) string {
	s = strings.ReplaceAll(s, "\u00a0", " ")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return strings.Join(strings.Fields(s), " ")
}

func isComposerDividerLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	count := 0
	for _, r := range line {
		if r == '─' || r == '-' || r == '━' {
			count++
			continue
		}
		return false
	}
	return count >= 10
}

func parsePromptFromComposerBlock(lines []string) (string, bool) {
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], " \t\r")
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" {
			continue
		}

		markerLen := 0
		for _, marker := range []string{"❯", "›"} {
			if strings.HasPrefix(trimmed, marker) {
				markerLen = len(marker)
				break
			}
		}
		if markerLen == 0 {
			continue
		}

		bodyParts := []string{strings.TrimSpace(trimmed[markerLen:])}
		for j := i + 1; j < len(lines); j++ {
			cont := strings.TrimRight(lines[j], " \t\r")
			if strings.TrimSpace(cont) == "" {
				if len(bodyParts) > 0 && bodyParts[len(bodyParts)-1] != "" {
					break
				}
				continue
			}
			// Wrapped composer lines are typically indented continuation lines.
			if strings.HasPrefix(cont, "  ") || strings.HasPrefix(cont, "\t") {
				bodyParts = append(bodyParts, strings.TrimSpace(cont))
				continue
			}
			break
		}

		return normalizePromptText(strings.Join(bodyParts, " ")), true
	}
	return "", false
}

func currentComposerPrompt(content string) (string, bool) {
	lines := strings.Split(content, "\n")
	if len(lines) > 240 {
		lines = lines[len(lines)-240:]
	}

	// Primary path: parse the explicit composer region between the last two
	// divider lines nearest the bottom of the pane.
	lastDivider := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if isComposerDividerLine(lines[i]) {
			lastDivider = i
			break
		}
	}
	if lastDivider > 0 {
		prevDivider := -1
		for i := lastDivider - 1; i >= 0; i-- {
			if isComposerDividerLine(lines[i]) {
				prevDivider = i
				break
			}
		}
		if prevDivider >= 0 && prevDivider+1 < lastDivider {
			if body, ok := parsePromptFromComposerBlock(lines[prevDivider+1 : lastDivider]); ok {
				return body, true
			}
		}
	}

	// Fallback for layouts without clear divider lines: look near the bottom
	// for a strict prompt marker at the start of the line.
	start := 0
	if len(lines) > 40 {
		start = len(lines) - 40
	}
	for i := len(lines) - 1; i >= start; i-- {
		trimmed := strings.TrimLeft(lines[i], " \t")
		if strings.TrimSpace(trimmed) == "" {
			continue
		}
		for _, marker := range []string{"❯", "›"} {
			if strings.HasPrefix(trimmed, marker) {
				return normalizePromptText(strings.TrimSpace(trimmed[len(marker):])), true
			}
		}
	}
	return "", false
}

func hasCurrentComposerPrompt(content string) bool {
	_, ok := currentComposerPrompt(content)
	return ok
}

// hasUnsentComposerPrompt detects when the message text is still present in the
// interactive input line (e.g., "❯ message"), which indicates Enter was not
// accepted yet even if no "[Pasted text ...]" marker is shown.
func hasUnsentComposerPrompt(content, message string) bool {
	msg := normalizePromptText(message)
	if msg == "" {
		return false
	}

	promptBody, hasPrompt := currentComposerPrompt(content)
	if !hasPrompt {
		return false
	}
	promptBody = normalizePromptText(promptBody)
	if promptBody == "" {
		return false
	}

	// Direct match (short prompts or fully visible single-line prompts).
	if strings.HasPrefix(promptBody, msg) || strings.Contains(promptBody, msg) {
		return true
	}

	// Wrapped prompts: Claude often shows only the first visual line of the
	// current composer input (message wraps to following indented lines).
	// If the visible prompt line is a substantial prefix of the message,
	// Enter was not accepted yet.
	const minWrappedPrefixLen = 16
	if len(promptBody) >= minWrappedPrefixLen && strings.HasPrefix(msg, promptBody) {
		return true
	}

	// Fallback: compare a short message prefix to handle truncation/formatting
	// differences while avoiding over-broad matching.
	needle := msg
	if len(needle) > 32 {
		needle = needle[:32]
	}
	if strings.Contains(promptBody, needle) {
		return true
	}

	return false
}

// errorRecheckInterval - how often to recheck sessions that don't exist
// Ghost sessions (in JSON but not in tmux) are rechecked at this interval
// instead of every 500ms tick, dramatically reducing subprocess spawns
const errorRecheckInterval = 30 * time.Second

func hookFastPathFreshnessForTool(tool, hookStatus string) time.Duration {
	if tool != "codex" {
		return hookFastPathWindow
	}

	// Codex hook events are turn-based and can be sparse depending on command mode.
	// Keep running freshness short, but preserve completion/waiting signals longer so
	// the user can reliably see attention-needed state.
	switch hookStatus {
	case "waiting":
		return codexHookWaitingFastPathWindow
	default:
		return codexHookRunningFastPathWindow
	}
}

// UpdateStatus updates the session status by checking tmux.
// Thread-safe: acquires write lock to protect Status, Tool, and internal cache fields.
func (i *Instance) UpdateStatus() error {
	i.mu.Lock()
	defer i.mu.Unlock()

	// Short grace period for tmux initialization (not Claude startup)
	// Use lastStartTime for accuracy on restarts, fallback to CreatedAt
	graceTime := i.lastStartTime
	if graceTime.IsZero() {
		graceTime = i.CreatedAt
	}
	// 1.5 seconds is enough for tmux to create the session (<100ms typically)
	// Don't block status detection once tmux session exists
	if time.Since(graceTime) < 1500*time.Millisecond {
		// Only skip if tmux session doesn't exist yet
		if i.tmuxSession == nil || !i.tmuxSession.Exists() {
			if i.Status != StatusRunning && i.Status != StatusIdle {
				i.Status = StatusStarting
			}
			return nil
		}
		// Session exists - allow normal status detection below
	}

	if i.tmuxSession == nil {
		i.Status = StatusError
		return nil
	}

	// Optimization: Skip expensive Exists() check for sessions already in error status
	// Ghost sessions (in JSON but not in tmux) only get rechecked every 30 seconds
	// This reduces subprocess spawns from 74/sec to ~5/sec for 28 ghost sessions
	if i.Status == StatusError && !i.lastErrorCheck.IsZero() &&
		time.Since(i.lastErrorCheck) < errorRecheckInterval {
		return nil // Skip - still in error, checked recently
	}

	// Check if tmux session exists
	if !i.tmuxSession.Exists() {
		i.Status = StatusError
		i.lastErrorCheck = time.Now() // Record when we confirmed error
		return nil
	}

	// Session exists - clear error check timestamp
	i.lastErrorCheck = time.Time{}

	// Tiered polling: skip expensive checks for idle sessions with no new activity
	if i.Status == StatusIdle {
		currentTS := i.tmuxSession.GetCachedWindowActivity()
		if currentTS == i.lastKnownActivity && !i.lastIdleCheck.IsZero() &&
			time.Since(i.lastIdleCheck) < 10*time.Second {
			return nil // No activity detected, skip full check
		}
		// Activity detected OR recheck interval passed: do full check
		i.lastIdleCheck = time.Now()
		i.lastKnownActivity = currentTS
	}

	// HOOK FAST PATH: hook-based status for tools that emit lifecycle events.
	// Freshness is tool- and state-specific (e.g. Codex running vs waiting).
	if (i.Tool == "claude" || i.Tool == "codex") &&
		i.hookStatus != "" &&
		time.Since(i.hookLastUpdate) < hookFastPathFreshnessForTool(i.Tool, i.hookStatus) {
		switch i.hookStatus {
		case "running":
			i.Status = StatusRunning
			// Reset acknowledged: new activity means output not yet seen.
			// Without this, a previously-acknowledged session would go straight
			// to idle (gray) after Stop, skipping the waiting (orange) state.
			if i.tmuxSession != nil {
				i.tmuxSession.ResetAcknowledged()
			}
		case "waiting":
			if i.Tool == "codex" {
				// Codex completion should surface as attention-needed.
				// Keep this as waiting and let tmux settle to idle if the user
				// has acknowledged and no new activity appears.
				if i.tmuxSession != nil {
					i.tmuxSession.ResetAcknowledged()
				}
				i.Status = StatusWaiting
			} else {
				// Check acknowledgment: orange (waiting) vs gray (idle)
				// Acknowledge() is called when user attaches to a session.
				// ResetAcknowledged() is called by u key or when new activity occurs.
				if i.tmuxSession != nil && i.tmuxSession.IsAcknowledged() {
					i.Status = StatusIdle
				} else {
					i.Status = StatusWaiting
				}
			}
		case "dead":
			i.Status = StatusError
		}
		if i.hookSessionID != "" {
			switch i.Tool {
			case "claude":
				if i.hookSessionID != i.ClaudeSessionID {
					i.ClaudeSessionID = i.hookSessionID
					i.ClaudeDetectedAt = time.Now()
				}
			case "codex":
				if i.hookSessionID != i.CodexSessionID {
					i.CodexSessionID = i.hookSessionID
					i.CodexDetectedAt = time.Now()
				}
			}
		}
		return nil
	}

	// Release lock for potentially slow tmux calls (GetStatus calls CapturePane)
	i.mu.Unlock()
	status, err := i.tmuxSession.GetStatus()
	i.mu.Lock()

	if err != nil {
		i.Status = StatusError
		return err
	}

	// Map tmux status to instance status
	switch status {
	case "active":
		i.Status = StatusRunning
	case "waiting":
		if i.Tool == "shell" {
			i.Status = StatusIdle
		} else {
			i.Status = StatusWaiting
		}
	case "idle":
		i.Status = StatusIdle
	case "starting":
		i.Status = StatusStarting
	case "inactive":
		i.Status = StatusError
	default:
		i.Status = StatusError
	}

	// Update tool detection dynamically (enables fork when Claude starts)
	if detectedTool := i.tmuxSession.DetectTool(); detectedTool != "" {
		i.Tool = detectedTool
	}

	// Update session tracking only for active/waiting sessions (skip idle - nothing changes)
	if i.Status == StatusRunning || i.Status == StatusWaiting {
		// Update Claude session tracking (non-blocking, best-effort)
		i.UpdateClaudeSession(nil)

		// Update Gemini session tracking (non-blocking, best-effort)
		if i.Tool == "gemini" {
			i.UpdateGeminiSession(nil)
		}

		// Update Codex session tracking (non-blocking, best-effort)
		if i.Tool == "codex" {
			var exclude map[string]bool
			if i.CodexSessionID == "" {
				exclude = i.collectOtherCodexSessionIDs()
			}
			i.UpdateCodexSession(exclude)
		}
	}

	return nil
}

// UpdateClaudeSession updates the Claude session ID from tmux environment.
// The capture-resume pattern (used in Start/Fork/Restart) sets CLAUDE_SESSION_ID
// in the tmux environment, making this the single authoritative source.
//
// No file scanning fallback - we rely on the consistent capture-resume pattern.
func (i *Instance) UpdateClaudeSession(excludeIDs map[string]bool) {
	if i.Tool != "claude" {
		return
	}

	// Read from tmux environment (set by capture-resume pattern)
	if sessionID := i.GetSessionIDFromTmux(); sessionID != "" {
		if i.ClaudeSessionID != sessionID {
			// Quality gate: don't adopt a zombie ID from tmux env when current has real data
			if i.ClaudeSessionID != "" {
				currentHasData := sessionHasConversationData(i.ClaudeSessionID, i.ProjectPath)
				candidateHasData := sessionHasConversationData(sessionID, i.ProjectPath)
				if currentHasData && !candidateHasData {
					sessionLog.Debug("claude_session_tmux_rejected_zombie",
						slog.String("current_id", i.ClaudeSessionID),
						slog.String("zombie_id", sessionID),
						slog.String("reason", "tmux_env_has_zombie_id"),
					)
					// Don't adopt the zombie; skip the update but still refresh prompt below
					sessionID = i.ClaudeSessionID
				}
			}
			i.ClaudeSessionID = sessionID
		}
		i.ClaudeDetectedAt = time.Now()
	}

	// Update latest prompt from JSONL file (tail-read with size caching)
	if i.ClaudeSessionID != "" {
		jsonlPath := i.GetJSONLPath()
		if jsonlPath != "" {
			if prompt := i.readJSONLTail(jsonlPath); prompt != "" {
				i.LatestPrompt = prompt
			}
		}
	}
}

// collectOtherClaudeSessionIDs enumerates all agent-deck tmux sessions (except this one)
// and returns the set of CLAUDE_SESSION_ID values they own. Used to avoid stealing
// another instance's session when scanning for the most recent .jsonl on disk.
func (i *Instance) collectOtherClaudeSessionIDs() map[string]bool {
	exclude := make(map[string]bool)

	tmuxSessions, err := tmux.ListAgentDeckSessions()
	if err != nil {
		return exclude
	}

	myTmuxName := ""
	if i.tmuxSession != nil {
		myTmuxName = i.tmuxSession.Name
	}

	for _, sessName := range tmuxSessions {
		if sessName == myTmuxName {
			continue
		}
		// Read CLAUDE_SESSION_ID from the other tmux session
		other := &tmux.Session{Name: sessName}
		if id, err := other.GetEnvironment("CLAUDE_SESSION_ID"); err == nil && id != "" {
			exclude[id] = true
		}
	}

	return exclude
}

// syncClaudeSessionFromDisk scans the filesystem for the most recent session file,
// excluding IDs owned by other agent-deck instances. If a different (newer) session
// is found, it updates ClaudeSessionID, ClaudeDetectedAt, and the tmux env var.
// This handles the case where /clear in Claude Code creates a new session UUID
// that the tmux env var doesn't know about yet.
func (i *Instance) syncClaudeSessionFromDisk() {
	if i.Tool != "claude" {
		return
	}

	configDir := GetClaudeConfigDir()
	exclude := i.collectOtherClaudeSessionIDs()

	activeID := findActiveSessionIDExcluding(configDir, i.ProjectPath, exclude)
	if activeID == "" || activeID == i.ClaudeSessionID {
		return
	}

	// Quality gate: don't replace a session with real conversation data with a zombie
	// (a zombie is a session file with no conversation data, typically from a crashed startup)
	if i.ClaudeSessionID != "" {
		currentHasData := sessionHasConversationData(i.ClaudeSessionID, i.ProjectPath)
		candidateHasData := sessionHasConversationData(activeID, i.ProjectPath)

		// Decision matrix:
		//   current=real, candidate=real   → ACCEPT (handles /clear: both real, newer wins)
		//   current=real, candidate=zombie  → REJECT (never replace real with zombie)
		//   current=zombie, candidate=real  → ACCEPT (upgrade from zombie to real)
		//   current=zombie, candidate=zombie → REJECT (don't swap zombies)
		if currentHasData && !candidateHasData {
			sessionLog.Debug("claude_session_sync_rejected_zombie",
				slog.String("current_id", i.ClaudeSessionID),
				slog.String("zombie_id", activeID),
				slog.String("reason", "candidate_has_no_conversation_data"),
			)
			return
		}
		if !currentHasData && !candidateHasData {
			sessionLog.Debug("claude_session_sync_rejected_zombie",
				slog.String("current_id", i.ClaudeSessionID),
				slog.String("candidate_id", activeID),
				slog.String("reason", "both_have_no_conversation_data"),
			)
			return
		}
	}

	sessionLog.Debug("claude_session_update_from_disk", slog.String("old_id", i.ClaudeSessionID), slog.String("new_id", activeID))
	i.ClaudeSessionID = activeID
	i.ClaudeDetectedAt = time.Now()

	// Sync back to tmux environment so restart uses the new ID
	if i.tmuxSession != nil && i.tmuxSession.Exists() {
		_ = i.tmuxSession.SetEnvironment("CLAUDE_SESSION_ID", activeID)
	}
}

// UpdateHookStatus updates the instance's hook-based status fields.
// Called by StatusFileWatcher when a hook status file changes.
func (i *Instance) UpdateHookStatus(status *HookStatus) {
	if status == nil {
		return
	}

	i.mu.Lock()
	defer i.mu.Unlock()

	i.hookStatus = status.Status
	i.hookLastUpdate = status.UpdatedAt

	// Sync session ID from hook if provided.
	if status.SessionID == "" {
		return
	}

	switch i.Tool {
	case "claude":
		if status.SessionID == i.ClaudeSessionID {
			return
		}
		// Quality gate: only accept if the hook session has conversation data,
		// OR if the current session ID is empty (first detection).
		if i.ClaudeSessionID == "" || sessionHasConversationData(status.SessionID, i.ProjectPath) {
			sessionLog.Debug("claude_session_update_from_hook",
				slog.String("old_id", i.ClaudeSessionID),
				slog.String("new_id", status.SessionID),
				slog.String("event", status.Event),
			)
			i.ClaudeSessionID = status.SessionID
			i.ClaudeDetectedAt = time.Now()
			i.hookSessionID = status.SessionID

			if i.tmuxSession != nil && i.tmuxSession.Exists() {
				_ = i.tmuxSession.SetEnvironment("CLAUDE_SESSION_ID", status.SessionID)
			}
		}
	case "codex":
		if status.SessionID == i.CodexSessionID {
			return
		}
		sessionLog.Debug("codex_session_update_from_hook",
			slog.String("old_id", i.CodexSessionID),
			slog.String("new_id", status.SessionID),
			slog.String("event", status.Event),
		)
		i.CodexSessionID = status.SessionID
		i.CodexDetectedAt = time.Now()
		i.hookSessionID = status.SessionID

		if i.tmuxSession != nil && i.tmuxSession.Exists() {
			_ = i.tmuxSession.SetEnvironment("CODEX_SESSION_ID", status.SessionID)
		}
	}
}

// GetHookStatus returns the current hook-based status and its freshness.
// Freshness window is tool-specific.
func (i *Instance) GetHookStatus() (string, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()

	if i.hookStatus == "" {
		return "", false
	}
	fresh := time.Since(i.hookLastUpdate) < hookFastPathFreshnessForTool(i.Tool, i.hookStatus)
	return i.hookStatus, fresh
}

// ClearHookStatus resets the hook-based status, forcing the next UpdateStatus()
// to fall through to polling. Used when the user manually overrides status (e.g., pressing 'u'
// to unacknowledge after an Escape interrupt where the Stop hook didn't fire).
func (i *Instance) ClearHookStatus() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.hookStatus = ""
	i.hookLastUpdate = time.Time{}
}

// ForceNextStatusCheck clears the idle polling optimization so the next
// UpdateStatus() performs a full check instead of short-circuiting.
// Call this before UpdateStatus() when a status-affecting change was made
// externally (e.g. the u key toggling acknowledged state).
func (i *Instance) ForceNextStatusCheck() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.lastIdleCheck = time.Time{}
}

// SetGeminiYoloMode sets the YOLO mode for Gemini and syncs it to the tmux environment.
// This ensures the background status worker sees the correct state during restarts.
func (i *Instance) SetGeminiYoloMode(enabled bool) {
	if i.Tool != "gemini" {
		return
	}

	i.GeminiYoloMode = &enabled

	// Sync to tmux environment immediately if session exists
	// This ensures background detection (UpdateGeminiSession) sees the new value
	if i.tmuxSession != nil && i.tmuxSession.Exists() {
		val := "false"
		if enabled {
			val = "true"
		}
		_ = i.tmuxSession.SetEnvironment("GEMINI_YOLO_MODE", val)
	}
}

// UpdateGeminiSession updates the Gemini session ID, YOLO mode, analytics, and latest prompt.
// Delegates to focused helpers for each concern.
func (i *Instance) UpdateGeminiSession(excludeIDs map[string]bool) {
	if i.Tool != "gemini" {
		return
	}
	i.syncGeminiSessionFromTmux()
	i.syncGeminiSessionFromDisk()
	i.updateGeminiAnalytics()
	i.updateGeminiLatestPrompt()
}

// syncGeminiSessionFromTmux reads session ID and YOLO mode from tmux environment (authoritative source).
func (i *Instance) syncGeminiSessionFromTmux() {
	if i.tmuxSession == nil {
		return
	}
	if sessionID, err := i.tmuxSession.GetEnvironment("GEMINI_SESSION_ID"); err == nil && sessionID != "" {
		if i.GeminiSessionID != sessionID {
			i.GeminiSessionID = sessionID
		}
		i.GeminiDetectedAt = time.Now()
	}

	// Detect YOLO Mode from environment (authoritative sync)
	if yoloEnv, err := i.tmuxSession.GetEnvironment("GEMINI_YOLO_MODE"); err == nil && yoloEnv != "" {
		enabled := yoloEnv == "true"
		i.GeminiYoloMode = &enabled
	}
}

// syncGeminiSessionFromDisk scans the filesystem for the most recent session.
// Krudony fix: user may have started a NEW session, so always scan rather than using stale cached ID.
func (i *Instance) syncGeminiSessionFromDisk() {
	sessions, err := ListGeminiSessions(i.ProjectPath)
	if err != nil || len(sessions) == 0 {
		return
	}

	// Pick the most recent session (list is sorted by LastUpdated desc)
	mostRecent := sessions[0]
	if mostRecent.SessionID != i.GeminiSessionID {
		sessionLog.Debug("gemini_session_update", slog.String("old_id", i.GeminiSessionID), slog.String("new_id", mostRecent.SessionID))
	}
	i.GeminiSessionID = mostRecent.SessionID
	i.GeminiDetectedAt = time.Now()

	// Sync back to tmux environment for future restarts
	if i.tmuxSession != nil && i.tmuxSession.Exists() {
		_ = i.tmuxSession.SetEnvironment("GEMINI_SESSION_ID", i.GeminiSessionID)
	}
}

// updateGeminiAnalytics refreshes token counts, cost, and model from the session file.
// Syncs the detected model back to the instance's GeminiModel field.
func (i *Instance) updateGeminiAnalytics() {
	if i.GeminiSessionID == "" {
		return
	}
	if i.GeminiAnalytics == nil {
		i.GeminiAnalytics = &GeminiSessionAnalytics{}
	}
	// Non-blocking update (ignore errors, best effort)
	_ = UpdateGeminiAnalyticsFromDisk(i.ProjectPath, i.GeminiSessionID, i.GeminiAnalytics)

	// Sync detected model from analytics to instance (if not explicitly set by user)
	if i.GeminiModel == "" && i.GeminiAnalytics.Model != "" {
		i.GeminiModel = i.GeminiAnalytics.Model
	}
}

// updateGeminiLatestPrompt extracts the latest user prompt from the session file.
// Uses mtime caching to skip re-reading unchanged files (important for large session files).
func (i *Instance) updateGeminiLatestPrompt() {
	if i.GeminiSessionID == "" || len(i.GeminiSessionID) < 8 {
		return
	}

	sessionsDir := GetGeminiSessionsDir(i.ProjectPath)
	pattern := filepath.Join(sessionsDir, "session-*-"+i.GeminiSessionID[:8]+".json")
	filePath, fileMtime := findNewestFile(pattern)

	// Fallback: cross-project search
	if filePath == "" {
		filePath = findGeminiSessionInAllProjects(i.GeminiSessionID)
		if filePath != "" {
			if info, err := os.Stat(filePath); err == nil {
				fileMtime = info.ModTime()
			}
		}
	}

	if filePath == "" {
		return
	}

	// mtime cache: skip re-read if file hasn't changed since last read
	if !i.lastPromptModTime.IsZero() && !fileMtime.IsZero() && fileMtime.Equal(i.lastPromptModTime) {
		return
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return
	}
	if prompt, err := parseGeminiLatestUserPrompt(data); err == nil && prompt != "" {
		i.LatestPrompt = prompt
	}
	i.lastPromptModTime = fileMtime
}

// WaitForClaudeSession waits for the tmux environment variable to be set.
// The capture-resume pattern sets CLAUDE_SESSION_ID in tmux env, so we poll for that.
// Returns the detected session ID or empty string after timeout.
func (i *Instance) WaitForClaudeSession(maxWait time.Duration) string {
	if i.Tool != "claude" {
		return ""
	}

	// Poll every 200ms for up to maxWait
	interval := 200 * time.Millisecond
	deadline := time.Now().Add(maxWait)

	for time.Now().Before(deadline) {
		// Check tmux environment (set by capture-resume pattern)
		if sessionID := i.GetSessionIDFromTmux(); sessionID != "" {
			i.ClaudeSessionID = sessionID
			i.ClaudeDetectedAt = time.Now()
			return sessionID
		}
		time.Sleep(interval)
	}

	return ""
}

// WaitForClaudeSessionWithExclude waits for the tmux environment variable to be set.
// The excludeIDs parameter is kept for API compatibility but not used since tmux env
// is authoritative and won't return duplicate IDs.
func (i *Instance) WaitForClaudeSessionWithExclude(maxWait time.Duration, excludeIDs map[string]bool) string {
	// tmux env is authoritative - no need for exclusion logic
	return i.WaitForClaudeSession(maxWait)
}

// PostStartSync captures session IDs from tmux environment after Start() or Restart().
// Designed for CLI commands that exit after starting. The TUI doesn't need this
// because its background worker handles detection.
//
// For Claude: polls tmux env for CLAUDE_SESSION_ID (set by bash uuidgen before exec).
// For Gemini: reads session ID from filesystem.
// For OpenCode/Codex: no-op (async goroutine detection, too slow for sync CLI).
func (i *Instance) PostStartSync(maxWait time.Duration) {
	switch i.Tool {
	case "claude":
		i.WaitForClaudeSession(maxWait)
	case "gemini":
		i.UpdateGeminiSession(nil)
	}
	// OpenCode/Codex: async detection already started by Start(), skip here
}

// Preview returns the last 3 lines of terminal output
func (i *Instance) Preview() (string, error) {
	if i.tmuxSession == nil {
		return "", fmt.Errorf("tmux session not initialized")
	}

	content, err := i.tmuxSession.CapturePane()
	if err != nil {
		return "", err
	}

	lines := strings.Split(strings.TrimSpace(content), "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}

	return strings.Join(lines, "\n"), nil
}

// PreviewFull returns all terminal output
func (i *Instance) PreviewFull() (string, error) {
	if i.tmuxSession == nil {
		return "", fmt.Errorf("tmux session not initialized")
	}

	return i.tmuxSession.CaptureFullHistory()
}

// HasUpdated checks if there's new output since last check
func (i *Instance) HasUpdated() bool {
	if i.tmuxSession == nil {
		return false
	}

	updated, err := i.tmuxSession.HasUpdated()
	if err != nil {
		return false
	}

	return updated
}

// SyncSessionIDsToTmux syncs session IDs from Instance to tmux environment.
// PERFORMANCE: This is called on-demand (e.g., first attach) rather than at load time
// to reduce subprocess overhead during TUI startup.
//
// Session IDs are needed in tmux environment for restart/resume operations that
// spawn new processes. Without this sync, R key wouldn't resume the correct session.
func (i *Instance) SyncSessionIDsToTmux() {
	if i.tmuxSession == nil || !i.tmuxSession.Exists() {
		return
	}

	// Sync ClaudeSessionID
	if i.ClaudeSessionID != "" {
		_ = i.tmuxSession.SetEnvironment("CLAUDE_SESSION_ID", i.ClaudeSessionID)
	}

	// Sync GeminiSessionID
	if i.GeminiSessionID != "" {
		_ = i.tmuxSession.SetEnvironment("GEMINI_SESSION_ID", i.GeminiSessionID)
	}

	// Sync OpenCodeSessionID
	if i.OpenCodeSessionID != "" {
		_ = i.tmuxSession.SetEnvironment("OPENCODE_SESSION_ID", i.OpenCodeSessionID)
	}

	// Sync CodexSessionID
	if i.CodexSessionID != "" {
		_ = i.tmuxSession.SetEnvironment("CODEX_SESSION_ID", i.CodexSessionID)
	}
}

// ResponseOutput represents a parsed response from an agent session
type ResponseOutput struct {
	Tool      string `json:"tool"`                 // Tool type (claude, gemini, etc.)
	Role      string `json:"role"`                 // Always "assistant" for now
	Content   string `json:"content"`              // The actual response text
	Timestamp string `json:"timestamp,omitempty"`  // When the response was generated (Claude only)
	SessionID string `json:"session_id,omitempty"` // Claude session ID (if available)
}

// GetLastResponse returns the last assistant response from the session
// For Claude: Parses the JSONL file for the last assistant message
// For Gemini: Parses the JSON session file for the last assistant message
// For Codex/Others: Attempts to parse terminal output
func (i *Instance) GetLastResponse() (*ResponseOutput, error) {
	if i.Tool == "claude" {
		return i.getClaudeLastResponse()
	}
	if i.Tool == "gemini" {
		return i.getGeminiLastResponse()
	}
	return i.getTerminalLastResponse()
}

// GetLastResponseBestEffort returns the last assistant response with fallback logic
// intended for CLI read paths (like `session output`) where we prefer useful output
// over hard errors.
//
// Behavior for Claude:
// 1. Try structured JSONL read via stored ClaudeSessionID.
// 2. Refresh ID from tmux env and retry.
// 3. Scan disk for active session ID and retry.
// 4. Fallback to terminal parsing.
// 5. If still unavailable, return an empty response (no error).
func (i *Instance) GetLastResponseBestEffort() (*ResponseOutput, error) {
	resp, err := i.GetLastResponse()
	if err == nil {
		return resp, nil
	}

	// Claude-specific recovery path
	if i.Tool == "claude" {
		// Refresh from tmux env (fast path)
		if sessionID := i.GetSessionIDFromTmux(); sessionID != "" {
			i.ClaudeSessionID = sessionID
			i.ClaudeDetectedAt = time.Now()
			if recovered, recoverErr := i.getClaudeLastResponse(); recoverErr == nil {
				return recovered, nil
			}
		}

		// Fallback: detect latest session on disk (handles startup race / stale ID)
		i.syncClaudeSessionFromDisk()
		if i.ClaudeSessionID != "" {
			if recovered, recoverErr := i.getClaudeLastResponse(); recoverErr == nil {
				return recovered, nil
			}
		}
	}

	// Final fallback: terminal parsing (works for all tools).
	if i.tmuxSession != nil {
		if terminalResp, terminalErr := i.getTerminalLastResponse(); terminalErr == nil {
			return terminalResp, nil
		}
	}

	// For Claude, prefer a graceful empty response instead of a hard error.
	if i.Tool == "claude" {
		return &ResponseOutput{
			Tool:    "claude",
			Role:    "assistant",
			Content: "",
		}, nil
	}

	return nil, err
}

// GetJSONLPath returns the path to the Claude session JSONL file for analytics
// Returns empty string if this is not a Claude session or no session ID is available
func (i *Instance) GetJSONLPath() string {
	if i.Tool != "claude" || i.ClaudeSessionID == "" {
		return ""
	}

	configDir := GetClaudeConfigDir()

	// Resolve symlinks in project path (macOS: /tmp -> /private/tmp)
	resolvedPath := i.ProjectPath
	if resolved, err := filepath.EvalSymlinks(i.ProjectPath); err == nil {
		resolvedPath = resolved
	}

	// Convert project path to Claude's directory format
	// Claude replaces ALL non-alphanumeric chars (spaces, !, etc.) with hyphens
	// /Users/master/Code cloud/!Project -> -Users-master-Code-cloud--Project
	projectDirName := ConvertToClaudeDirName(resolvedPath)
	projectDir := filepath.Join(configDir, "projects", projectDirName)

	// Build the JSONL file path
	sessionFile := filepath.Join(projectDir, i.ClaudeSessionID+".jsonl")

	// Verify file exists before returning
	if _, err := os.Stat(sessionFile); os.IsNotExist(err) {
		return ""
	}

	return sessionFile
}

// getClaudeLastResponse extracts the last assistant message from Claude's JSONL file
func (i *Instance) getClaudeLastResponse() (*ResponseOutput, error) {
	// Require stored session ID - no fallback to file scanning
	if i.ClaudeSessionID == "" {
		return nil, fmt.Errorf("no Claude session ID available for this instance")
	}

	configDir := GetClaudeConfigDir()

	// Resolve symlinks in project path (macOS: /tmp -> /private/tmp)
	resolvedPath := i.ProjectPath
	if resolved, err := filepath.EvalSymlinks(i.ProjectPath); err == nil {
		resolvedPath = resolved
	}

	// Convert project path to Claude's directory format
	// Claude replaces ALL non-alphanumeric chars (spaces, !, etc.) with hyphens
	// /Users/master/Code cloud/!Project -> -Users-master-Code-cloud--Project
	projectDirName := ConvertToClaudeDirName(resolvedPath)
	projectDir := filepath.Join(configDir, "projects", projectDirName)

	// Use stored session ID directly
	sessionFile := filepath.Join(projectDir, i.ClaudeSessionID+".jsonl")

	// Check file exists
	if _, err := os.Stat(sessionFile); os.IsNotExist(err) {
		return nil, fmt.Errorf("session file not found: %s", sessionFile)
	}

	// Read and parse the JSONL file
	data, err := os.ReadFile(sessionFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}

	return parseClaudeLastAssistantMessage(data, filepath.Base(sessionFile))
}

// parseClaudeLastAssistantMessage parses a Claude JSONL file to extract the last assistant message
func parseClaudeLastAssistantMessage(data []byte, sessionID string) (*ResponseOutput, error) {
	// JSONL record structure (same as global_search.go)
	type claudeMessage struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	type claudeRecord struct {
		SessionID string          `json:"sessionId"`
		Type      string          `json:"type"`
		Message   json.RawMessage `json:"message"`
		Timestamp string          `json:"timestamp"`
	}

	var lastAssistantContent string
	var lastTimestamp string
	var foundSessionID string

	scanner := bufio.NewScanner(bytes.NewReader(data))
	// Handle large lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var record claudeRecord
		if err := json.Unmarshal(line, &record); err != nil {
			continue // Skip malformed lines
		}

		// Capture session ID
		if foundSessionID == "" && record.SessionID != "" {
			foundSessionID = record.SessionID
		}

		// Only care about messages
		if len(record.Message) == 0 {
			continue
		}

		var msg claudeMessage
		if err := json.Unmarshal(record.Message, &msg); err != nil {
			continue
		}

		// Only care about assistant messages
		if msg.Role != "assistant" {
			continue
		}

		// Extract content (can be string or array of blocks)
		var contentStr string
		var extractedText string
		if err := json.Unmarshal(msg.Content, &contentStr); err == nil {
			// Simple string content
			extractedText = contentStr
		} else {
			// Try as array of content blocks
			var blocks []map[string]interface{}
			if err := json.Unmarshal(msg.Content, &blocks); err == nil {
				var sb strings.Builder
				for _, block := range blocks {
					// Check for text type blocks
					if blockType, ok := block["type"].(string); ok && blockType == "text" {
						if text, ok := block["text"].(string); ok {
							sb.WriteString(text)
							sb.WriteString("\n")
						}
					}
				}
				extractedText = strings.TrimSpace(sb.String())
			}
		}
		// Only update if we found actual text content
		if extractedText != "" {
			lastAssistantContent = extractedText
			lastTimestamp = record.Timestamp
		}
	}

	if lastAssistantContent == "" {
		return nil, fmt.Errorf("no assistant response found in session")
	}

	return &ResponseOutput{
		Tool:      "claude",
		Role:      "assistant",
		Content:   lastAssistantContent,
		Timestamp: lastTimestamp,
		SessionID: foundSessionID,
	}, nil
}

// parseClaudeLatestUserPrompt parses a Claude JSONL file to extract the last user message
func parseClaudeLatestUserPrompt(data []byte) (string, error) {
	// JSONL record structure
	type claudeMessage struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	type claudeRecord struct {
		Message json.RawMessage `json:"message"`
	}

	var latestPrompt string

	scanner := bufio.NewScanner(bytes.NewReader(data))
	// Handle large lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var record claudeRecord
		if err := json.Unmarshal(line, &record); err != nil {
			continue // Skip malformed lines
		}

		// Only care about messages
		if len(record.Message) == 0 {
			continue
		}

		var msg claudeMessage
		if err := json.Unmarshal(record.Message, &msg); err != nil {
			continue
		}

		// Only care about user messages
		if msg.Role != "user" {
			continue
		}

		// Extract content (can be string or array of blocks)
		var contentStr string
		var extractedText string
		if err := json.Unmarshal(msg.Content, &contentStr); err == nil {
			// Simple string content
			extractedText = contentStr
		} else {
			// Try as array of content blocks
			var blocks []map[string]interface{}
			if err := json.Unmarshal(msg.Content, &blocks); err == nil {
				var sb strings.Builder
				for _, block := range blocks {
					if blockType, ok := block["type"].(string); ok && blockType == "text" {
						if text, ok := block["text"].(string); ok {
							sb.WriteString(text)
							sb.WriteString(" ")
						}
					}
				}
				extractedText = strings.TrimSpace(sb.String())
			}
		}

		// Sanitize: strip newlines and extra spaces for single-line display
		if extractedText != "" {
			content := strings.ReplaceAll(extractedText, "\n", " ")
			latestPrompt = strings.Join(strings.Fields(content), " ")
		}
	}

	return latestPrompt, nil
}

// readJSONLTail reads the last user prompt from a JSONL file using tail-read with size caching.
// Instead of reading the entire file (can be 100-800MB), it:
// 1. Stats the file to get current size (cheap syscall)
// 2. Skips reading entirely if size hasn't changed since last check
// 3. Only reads the last 32KB when the file has grown
func (i *Instance) readJSONLTail(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	size := info.Size()

	// If same file and same size, return cached prompt
	if path == i.lastJSONLPath && size == i.lastJSONLSize {
		return i.cachedPrompt
	}

	// File changed or new file - read the tail
	const tailSize int64 = 32 * 1024 // 32KB
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	offset := size - tailSize
	if offset < 0 {
		offset = 0
	}
	if offset > 0 {
		if _, err := f.Seek(offset, 0); err != nil {
			return ""
		}
	}

	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}

	// If we seeked into the middle of the file, skip to the first complete line
	if offset > 0 {
		if idx := bytes.IndexByte(data, '\n'); idx >= 0 {
			data = data[idx+1:]
		}
	}

	prompt, err := parseClaudeLatestUserPrompt(data)
	if err != nil || prompt == "" {
		// Update cache even on empty result to avoid re-reading
		i.lastJSONLPath = path
		i.lastJSONLSize = size
		return i.cachedPrompt // Return previous cached value
	}

	i.lastJSONLPath = path
	i.lastJSONLSize = size
	i.cachedPrompt = prompt
	return prompt
}

// parseGeminiLatestUserPrompt parses a Gemini JSON file to extract the last user message
func parseGeminiLatestUserPrompt(data []byte) (string, error) {
	var session struct {
		Messages []struct {
			Type    string `json:"type"` // "user" or "gemini"
			Content string `json:"content"`
		} `json:"messages"`
	}

	if err := json.Unmarshal(data, &session); err != nil {
		return "", fmt.Errorf("failed to parse Gemini session: %w", err)
	}

	var latestPrompt string
	// Find last "user" type message
	for i := len(session.Messages) - 1; i >= 0; i-- {
		msg := session.Messages[i]
		if msg.Type == "user" {
			// Sanitize: strip newlines and extra spaces for single-line display
			content := strings.ReplaceAll(msg.Content, "\n", " ")
			latestPrompt = strings.Join(strings.Fields(content), " ")
			break
		}
	}

	return latestPrompt, nil
}

// getGeminiLastResponse extracts the last assistant message from Gemini's JSON file
func (i *Instance) getGeminiLastResponse() (*ResponseOutput, error) {
	// Require stored session ID - no fallback to file scanning
	if i.GeminiSessionID == "" || len(i.GeminiSessionID) < 8 {
		return nil, fmt.Errorf("no Gemini session ID available for this instance")
	}

	sessionsDir := GetGeminiSessionsDir(i.ProjectPath)

	// Find file by session ID (first 8 chars in filename)
	// Filename format is session-YYYY-MM-DDTHH-MM-<uuid8>.json
	pattern := filepath.Join(sessionsDir, "session-*-"+i.GeminiSessionID[:8]+".json")
	files, _ := filepath.Glob(pattern)

	// Fallback: cross-project search if not found in expected location
	if len(files) == 0 {
		if fallbackPath := findGeminiSessionInAllProjects(i.GeminiSessionID); fallbackPath != "" {
			files = []string{fallbackPath}
		}
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("session file not found for ID: %s", i.GeminiSessionID)
	}
	sessionFile := files[0]

	// Read and parse the JSON file
	data, err := os.ReadFile(sessionFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}

	return parseGeminiLastAssistantMessage(data)
}

// parseGeminiLastAssistantMessage parses a Gemini JSON file to extract the last assistant message
// VERIFIED: Message type is "gemini" (NOT role: "assistant")
func parseGeminiLastAssistantMessage(data []byte) (*ResponseOutput, error) {
	var session struct {
		SessionID string `json:"sessionId"` // VERIFIED: camelCase
		Messages  []struct {
			ID        string            `json:"id"`
			Timestamp string            `json:"timestamp"`
			Type      string            `json:"type"` // VERIFIED: "user" or "gemini"
			Content   string            `json:"content"`
			ToolCalls []json.RawMessage `json:"toolCalls,omitempty"`
			Thoughts  []json.RawMessage `json:"thoughts,omitempty"`
			Model     string            `json:"model,omitempty"`
			Tokens    json.RawMessage   `json:"tokens,omitempty"`
		} `json:"messages"`
	}

	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("failed to parse session file: %w", err)
	}

	// Find last "gemini" type message
	for i := len(session.Messages) - 1; i >= 0; i-- {
		msg := session.Messages[i]
		if msg.Type == "gemini" {
			return &ResponseOutput{
				Tool:      "gemini",
				Role:      "assistant",
				Content:   msg.Content,
				Timestamp: msg.Timestamp,
				SessionID: session.SessionID,
			}, nil
		}
	}

	return nil, fmt.Errorf("no assistant response found in session")
}

// getTerminalLastResponse extracts the last response from terminal output
// This is used for Gemini, Codex, and other tools without structured output
func (i *Instance) getTerminalLastResponse() (*ResponseOutput, error) {
	if i.tmuxSession == nil {
		return nil, fmt.Errorf("tmux session not initialized")
	}

	// Capture full history
	content, err := i.tmuxSession.CaptureFullHistory()
	if err != nil {
		return nil, fmt.Errorf("failed to capture terminal output: %w", err)
	}

	// Parse based on tool type
	switch i.Tool {
	case "gemini":
		return parseGeminiOutput(content)
	case "codex":
		return parseCodexOutput(content)
	default:
		return parseGenericOutput(content, i.Tool)
	}
}

// parseGeminiOutput parses Gemini CLI output to extract the last response
func parseGeminiOutput(content string) (*ResponseOutput, error) {
	lines := strings.Split(content, "\n")

	// Gemini typically shows responses after "▸" prompt and before the next ">"
	// Look for response blocks in reverse order
	var responseLines []string
	inResponse := false

	for idx := len(lines) - 1; idx >= 0; idx-- {
		line := lines[idx]
		trimmed := strings.TrimSpace(line)

		// Skip empty lines at the end
		if trimmed == "" && !inResponse {
			continue
		}

		// Detect prompt line (end of response when reading backwards)
		// Common prompts: "> ", ">>> ", "$", "❯", "➜"
		isPrompt := regexp.MustCompile(`^(>|>>>|\$|❯|➜|gemini>)\s*$`).MatchString(trimmed)

		if isPrompt && inResponse {
			// We've found the start of the response block
			break
		}

		// Detect user input line (also marks start of assistant response when reading backwards)
		if strings.HasPrefix(trimmed, "> ") && len(trimmed) > 5 && inResponse {
			break
		}

		// We're in a response
		inResponse = true
		responseLines = append([]string{line}, responseLines...)
	}

	if len(responseLines) == 0 {
		return nil, fmt.Errorf("no response found in Gemini output")
	}

	// Clean up the response
	response := strings.TrimSpace(strings.Join(responseLines, "\n"))
	// Remove ANSI codes
	ansiRegex := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	response = ansiRegex.ReplaceAllString(response, "")

	return &ResponseOutput{
		Tool:    "gemini",
		Role:    "assistant",
		Content: response,
	}, nil
}

// parseCodexOutput parses OpenAI Codex CLI output
func parseCodexOutput(content string) (*ResponseOutput, error) {
	// Codex has similar structure - adapt as needed
	return parseGenericOutput(content, "codex")
}

// parseGenericOutput is a fallback parser for unknown tools
func parseGenericOutput(content, tool string) (*ResponseOutput, error) {
	lines := strings.Split(content, "\n")

	// Look for the last substantial block of text (more than 2 lines)
	// before a prompt character
	var responseLines []string
	inResponse := false
	promptPattern := regexp.MustCompile(`^[\s]*(>|>>>|\$|❯|➜|#|%)\s*$`)

	for idx := len(lines) - 1; idx >= 0; idx-- {
		line := lines[idx]
		trimmed := strings.TrimSpace(line)

		// Skip empty lines at the end
		if trimmed == "" && !inResponse {
			continue
		}

		// Detect prompt line
		if promptPattern.MatchString(trimmed) {
			if inResponse {
				break
			}
			continue
		}

		inResponse = true
		responseLines = append([]string{line}, responseLines...)

		// Stop if we've collected enough lines (limit to prevent huge outputs)
		if len(responseLines) > 500 {
			break
		}
	}

	if len(responseLines) == 0 {
		return nil, fmt.Errorf("no response found in terminal output")
	}

	// Clean up
	response := strings.TrimSpace(strings.Join(responseLines, "\n"))
	ansiRegex := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	response = ansiRegex.ReplaceAllString(response, "")

	return &ResponseOutput{
		Tool:    tool,
		Role:    "assistant",
		Content: response,
	}, nil
}

// Kill terminates the tmux session
func (i *Instance) Kill() error {
	if i.tmuxSession == nil {
		return fmt.Errorf("tmux session not initialized")
	}

	if err := i.tmuxSession.Kill(); err != nil {
		return fmt.Errorf("failed to kill tmux session: %w", err)
	}
	i.Status = StatusError

	// Clear cached data to free memory (Instance stays referenced in storage)
	i.cachedPrompt = ""
	i.lastJSONLPath = ""
	i.lastJSONLSize = 0
	i.LatestPrompt = ""

	return nil
}

// Restart restarts the Claude session
// For Claude sessions with known ID: sends Ctrl+C twice and resume command to existing session
// For dead sessions or unknown ID: recreates the tmux session
func (i *Instance) Restart() error {
	mcpLog.Debug("restart_called", slog.String("tool", i.Tool), slog.String("claude_session_id", i.ClaudeSessionID), slog.Bool("tmux_session", i.tmuxSession != nil), slog.Bool("tmux_exists", i.tmuxSession != nil && i.tmuxSession.Exists()))

	// Clear flag immediately to prevent it staying set if restart fails
	skipRegen := i.SkipMCPRegenerate
	i.SkipMCPRegenerate = false

	// Regenerate .mcp.json before restart to use socket pool if available
	// Skip if MCP dialog just wrote the config (avoids race condition)
	if i.Tool == "claude" && !skipRegen {
		if err := i.regenerateMCPConfig(); err != nil {
			mcpLog.Warn("mcp_config_regen_failed", slog.String("error", err.Error()))
			// Continue with restart - Claude will use existing .mcp.json or defaults
		}
	} else if skipRegen {
		mcpLog.Debug("mcp_regen_skipped", slog.String("reason", "flag_set_by_apply"))
	}

	// Sync Claude session from disk before restart to pick up /clear session changes
	if i.Tool == "claude" {
		i.syncClaudeSessionFromDisk()
	}

	// If Claude session with known ID AND tmux session exists, use respawn-pane
	if i.Tool == "claude" && i.ClaudeSessionID != "" && i.tmuxSession != nil && i.tmuxSession.Exists() {
		// Build the resume command with proper config
		resumeCmd := i.buildClaudeResumeCommand()
		resumeCmd, err := i.applyWrapper(resumeCmd)
		if err != nil {
			return err
		}
		mcpLog.Debug("respawn_pane_claude", slog.String("command", resumeCmd))

		// Use respawn-pane for atomic restart
		// This is more reliable than Ctrl+C + wait for shell + send command
		// respawn-pane -k kills the current process and starts the new command atomically
		if err := i.tmuxSession.RespawnPane(resumeCmd); err != nil {
			mcpLog.Debug("respawn_pane_claude_failed", slog.String("error", err.Error()))
			return fmt.Errorf("failed to restart Claude session: %w", err)
		}

		mcpLog.Debug("respawn_pane_claude_succeeded")

		// Re-capture MCPs after restart (they may have changed since session started)
		i.CaptureLoadedMCPs()

		// Start as WAITING - will go GREEN on next tick if Claude shows busy indicator
		i.Status = StatusWaiting
		return nil
	}

	// For Gemini: ALWAYS update session to get the most recent one
	// Krudony fix: don't skip when we already have an ID - the user may have started a NEW session
	if i.Tool == "gemini" {
		i.UpdateGeminiSession(nil)
	}

	// If Gemini session with known ID AND tmux session exists, use respawn-pane
	if i.Tool == "gemini" && i.GeminiSessionID != "" && i.tmuxSession != nil && i.tmuxSession.Exists() {
		resumeCmd := i.buildGeminiCommand("gemini")
		resumeCmd, err := i.applyWrapper(resumeCmd)
		if err != nil {
			return err
		}
		sessionLog.Info("restart_gemini_respawn", slog.String("command", resumeCmd))

		if err := i.tmuxSession.RespawnPane(resumeCmd); err != nil {
			sessionLog.Info("restart_gemini_respawn_failed", slog.String("error", err.Error()))
			return fmt.Errorf("failed to restart Gemini session: %w", err)
		}

		sessionLog.Info("restart_gemini_respawn_succeeded")
		i.Status = StatusWaiting
		return nil
	}

	// If OpenCode session AND tmux session exists, use respawn-pane
	if i.Tool == "opencode" && i.tmuxSession != nil && i.tmuxSession.Exists() {
		// Try to get session ID from tmux environment if not already set
		// (async detection stores it there but Instance might not have been saved)
		if i.OpenCodeSessionID == "" {
			if envID, err := i.tmuxSession.GetEnvironment("OPENCODE_SESSION_ID"); err == nil && envID != "" {
				i.OpenCodeSessionID = envID
				i.OpenCodeDetectedAt = time.Now()
				sessionLog.Info("restart_opencode_recovered_id", slog.String("session_id", envID))
			}
		}

		var resumeCmd string
		if i.OpenCodeSessionID != "" {
			// Resume with known session ID
			resumeCmd = fmt.Sprintf("tmux set-environment OPENCODE_SESSION_ID %s && opencode -s %s",
				i.OpenCodeSessionID, i.OpenCodeSessionID)
		} else {
			// No session ID yet, start fresh (will detect ID async)
			resumeCmd = "opencode"
			// Re-record start time for async detection
			i.OpenCodeStartedAt = time.Now().UnixMilli()
		}
		resumeCmd, err := i.applyWrapper(resumeCmd)
		if err != nil {
			return err
		}
		sessionLog.Info("restart_opencode_respawn", slog.String("command", resumeCmd))

		if err := i.tmuxSession.RespawnPane(resumeCmd); err != nil {
			sessionLog.Info("restart_opencode_respawn_failed", slog.String("error", err.Error()))
			return fmt.Errorf("failed to restart OpenCode session: %w", err)
		}

		// If no session ID, start async detection
		if i.OpenCodeSessionID == "" {
			go i.detectOpenCodeSessionAsync()
		}

		sessionLog.Info("restart_opencode_respawn_succeeded")
		i.Status = StatusWaiting
		return nil
	}

	// For Codex: ALWAYS update session to get the most recent one
	// Krudony fix: don't skip when we already have an ID - the user may have started a NEW session
	if i.Tool == "codex" {
		i.UpdateCodexSession(nil)
	}

	// If Codex session AND tmux session exists, use respawn-pane
	if i.Tool == "codex" && i.tmuxSession != nil && i.tmuxSession.Exists() {
		// Try to get session ID from tmux environment if not already set
		if i.CodexSessionID == "" {
			if envID, err := i.tmuxSession.GetEnvironment("CODEX_SESSION_ID"); err == nil && envID != "" {
				i.CodexSessionID = envID
				i.CodexDetectedAt = time.Now()
				sessionLog.Info("restart_codex_recovered_id", slog.String("session_id", envID))
			}
		}

		resumeCmd := i.buildCodexCommand("codex")
		if i.CodexSessionID == "" {
			// No session ID yet, start fresh (will detect ID async)
			// Re-record start time for async detection
			i.CodexStartedAt = time.Now().UnixMilli()
		}
		resumeCmd, err := i.applyWrapper(resumeCmd)
		if err != nil {
			return err
		}
		sessionLog.Info("restart_codex_respawn", slog.String("command", resumeCmd))

		if err := i.tmuxSession.RespawnPane(resumeCmd); err != nil {
			sessionLog.Info("restart_codex_respawn_failed", slog.String("error", err.Error()))
			return fmt.Errorf("failed to restart Codex session: %w", err)
		}

		// If no session ID, start async detection
		if i.CodexSessionID == "" {
			go i.detectCodexSessionAsync()
		}

		sessionLog.Info("restart_codex_respawn_succeeded")
		i.Status = StatusWaiting
		return nil
	}

	// If custom tool with session resume support AND tmux session exists, use respawn-pane
	if i.CanRestartGeneric() && i.tmuxSession != nil && i.tmuxSession.Exists() {
		toolDef := GetToolDef(i.Tool)
		sessionID := i.GetGenericSessionID()

		// Build resume command for custom tool
		var resumeCmd string
		if toolDef.DangerousMode && toolDef.DangerousFlag != "" {
			resumeCmd = fmt.Sprintf("tmux set-environment %s %s && %s %s %s %s",
				toolDef.SessionIDEnv, sessionID,
				i.Command, toolDef.ResumeFlag, sessionID, toolDef.DangerousFlag)
		} else {
			resumeCmd = fmt.Sprintf("tmux set-environment %s %s && %s %s %s",
				toolDef.SessionIDEnv, sessionID,
				i.Command, toolDef.ResumeFlag, sessionID)
		}
		resumeCmd, err := i.applyWrapper(resumeCmd)
		if err != nil {
			return err
		}

		sessionLog.Info("restart_generic_respawn", slog.String("tool", i.Tool), slog.String("command", resumeCmd))

		if err := i.tmuxSession.RespawnPane(resumeCmd); err != nil {
			sessionLog.Info("restart_generic_respawn_failed", slog.String("tool", i.Tool), slog.String("error", err.Error()))
			return fmt.Errorf("failed to restart %s session: %w", i.Tool, err)
		}

		sessionLog.Info("restart_generic_respawn_succeeded", slog.String("tool", i.Tool))
		i.loadCustomPatternsFromConfig() // Reload custom patterns
		i.Status = StatusWaiting
		return nil
	}

	mcpLog.Debug("restart_fallback_recreate")

	// Kill old tmux session to prevent orphans before recreating (#138)
	if i.tmuxSession != nil && i.tmuxSession.Exists() {
		mcpLog.Debug("restart_killing_old_session", slog.String("session_name", i.tmuxSession.Name))
		if killErr := i.tmuxSession.Kill(); killErr != nil {
			mcpLog.Warn("restart_kill_old_session_failed", slog.String("error", killErr.Error()))
		}
	}

	// Fallback: recreate tmux session (for dead sessions or unknown ID)
	i.tmuxSession = tmux.NewSession(i.Title, i.ProjectPath)
	i.tmuxSession.InstanceID = i.ID // Pass instance ID for activity hooks
	i.tmuxSession.SetInjectStatusLine(GetTmuxSettings().GetInjectStatusLine())

	var command string
	if i.Tool == "claude" && i.ClaudeSessionID != "" {
		command = i.buildClaudeResumeCommand()
	} else if i.Tool == "gemini" && i.GeminiSessionID != "" {
		command = i.buildGeminiCommand("gemini")
	} else if i.Tool == "opencode" && i.OpenCodeSessionID != "" {
		// Set OPENCODE_SESSION_ID in tmux env so detection works after restart
		command = fmt.Sprintf("tmux set-environment OPENCODE_SESSION_ID %s && opencode -s %s",
			i.OpenCodeSessionID, i.OpenCodeSessionID)
	} else if i.Tool == "codex" && i.CodexSessionID != "" {
		command = i.buildCodexCommand("codex")
	} else {
		// Route to appropriate command builder based on tool
		switch i.Tool {
		case "claude":
			command = i.buildClaudeCommand(i.Command)
		case "gemini":
			command = i.buildGeminiCommand(i.Command)
		case "opencode":
			command = i.buildOpenCodeCommand(i.Command)
			// Record start time for async session ID detection
			i.OpenCodeStartedAt = time.Now().UnixMilli()
		case "codex":
			command = i.buildCodexCommand(i.Command)
			// Record start time for async session ID detection
			i.CodexStartedAt = time.Now().UnixMilli()
		default:
			// Check if this is a custom tool with session resume config
			if toolDef := GetToolDef(i.Tool); toolDef != nil {
				command = i.buildGenericCommand(i.Command)
			} else {
				command = i.Command
			}
		}
	}
	command, err := i.applyWrapper(command)
	if err != nil {
		return err
	}

	// Load custom patterns for status detection (for custom tools)
	i.loadCustomPatternsFromConfig()

	// Apply user tmux option overrides from config (e.g. allow-passthrough = "all")
	if tmuxCfg := GetTmuxSettings(); len(tmuxCfg.Options) > 0 {
		i.tmuxSession.OptionOverrides = tmuxCfg.Options
	}

	mcpLog.Debug("restart_starting_new_session", slog.String("command", command))

	if err := i.tmuxSession.Start(command); err != nil {
		mcpLog.Debug("restart_start_failed", slog.String("error", err.Error()))
		i.Status = StatusError
		return fmt.Errorf("failed to restart tmux session: %w", err)
	}

	mcpLog.Debug("restart_start_succeeded")

	// Set AGENTDECK_INSTANCE_ID for Claude hooks to identify this session
	// This enables real-time status updates via Stop/SessionStart hooks
	if err := i.tmuxSession.SetEnvironment("AGENTDECK_INSTANCE_ID", i.ID); err != nil {
		sessionLog.Warn("set_instance_id_failed", slog.String("error", err.Error()))
	}

	// Re-capture MCPs after restart
	i.CaptureLoadedMCPs()

	// Start async session ID detection for OpenCode (if no ID yet)
	if i.Tool == "opencode" && i.OpenCodeSessionID == "" {
		go i.detectOpenCodeSessionAsync()
	}

	// Start async session ID detection for Codex (if no ID yet)
	if i.Tool == "codex" && i.CodexSessionID == "" {
		go i.detectCodexSessionAsync()
	}

	// Start as WAITING - will go GREEN on next tick if Claude shows busy indicator
	if command != "" {
		i.Status = StatusWaiting
	} else {
		i.Status = StatusIdle
	}

	return nil
}

// buildClaudeResumeCommand builds the claude resume command with proper config options
// Respects: CLAUDE_CONFIG_DIR, dangerous_mode from user config
// IMPORTANT: Also sets CLAUDE_SESSION_ID in tmux environment so detection works after restart
func (i *Instance) buildClaudeResumeCommand() string {
	// Get the configured Claude command (e.g., "claude", "cdw", "cdp")
	// If a custom command is set, we skip CLAUDE_CONFIG_DIR prefix since the alias handles it
	claudeCmd := GetClaudeCommand()
	hasCustomCommand := claudeCmd != "claude"

	// Check if CLAUDE_CONFIG_DIR is explicitly configured
	// If NOT explicit, don't set it - let the shell's environment handle it
	// Also skip if using a custom command (alias handles config dir)
	configDirPrefix := ""
	if !hasCustomCommand && IsClaudeConfigDirExplicit() {
		configDir := GetClaudeConfigDir()
		configDirPrefix = fmt.Sprintf("CLAUDE_CONFIG_DIR=%s ", configDir)
	}

	// AGENTDECK_INSTANCE_ID is set as an inline env var so hook subprocesses
	// can identify which agent-deck session they belong to.
	instanceIDPrefix := fmt.Sprintf("AGENTDECK_INSTANCE_ID=%s ", i.ID)
	configDirPrefix = instanceIDPrefix + configDirPrefix

	// Get per-session permission settings (falls back to config if not persisted)
	opts := i.GetClaudeOptions()
	if opts == nil {
		userConfig, _ := LoadUserConfig()
		opts = NewClaudeOptions(userConfig)
	}
	dangerousMode := opts.SkipPermissions
	allowDangerousMode := opts.AllowSkipPermissions

	// Check if session has actual conversation data
	// If not, use --session-id instead of --resume to avoid "No conversation found" error
	useResume := sessionHasConversationData(i.ClaudeSessionID, i.ProjectPath)
	sessionLog.Debug("session_data_build_resume", slog.String("session_id", i.ClaudeSessionID), slog.String("path", i.ProjectPath), slog.Bool("use_resume", useResume))

	// Build dangerous mode flag (--dangerously-skip-permissions wins over --allow-...)
	dangerousFlag := ""
	if dangerousMode {
		dangerousFlag = " --dangerously-skip-permissions"
	} else if allowDangerousMode {
		dangerousFlag = " --allow-dangerously-skip-permissions"
	}

	// Build the command with tmux environment update
	// This ensures CLAUDE_SESSION_ID is set in tmux env after restart,
	// so GetSessionIDFromTmux() works correctly and detects the session
	if useResume {
		return fmt.Sprintf("tmux set-environment CLAUDE_SESSION_ID %s && %s%s --resume %s%s",
			i.ClaudeSessionID, configDirPrefix, claudeCmd, i.ClaudeSessionID, dangerousFlag)
	}
	// Session was never interacted with - use --session-id to create fresh session
	return fmt.Sprintf("tmux set-environment CLAUDE_SESSION_ID %s && %s%s --session-id %s%s",
		i.ClaudeSessionID, configDirPrefix, claudeCmd, i.ClaudeSessionID, dangerousFlag)
}

// SetGeminiModel sets the Gemini model for this session and triggers a restart if running.
func (i *Instance) SetGeminiModel(model string) error {
	i.GeminiModel = model
	sessionLog.Debug("gemini_model_set", slog.String("model", model), slog.String("session_id", i.ID), slog.String("title", i.Title))

	// Restart if the session is running so it picks up the new model
	if i.Exists() {
		return i.Restart()
	}
	return nil
}

// CanRestart returns true if the session can be restarted
// For Claude sessions with known ID: can always restart (interrupt and resume)
// For Gemini sessions with known ID: can always restart (interrupt and resume)
// For OpenCode sessions with known ID: can always restart (interrupt and resume)
// For Codex sessions with known ID: can always restart (interrupt and resume)
// For custom tools with session resume config: can restart if session ID available
// For other sessions: only if dead/error state
func (i *Instance) CanRestart() bool {
	// Gemini sessions with known session ID can always be restarted
	if i.Tool == "gemini" && i.GeminiSessionID != "" {
		return true
	}

	// Claude sessions with known session ID can always be restarted
	if i.Tool == "claude" && i.ClaudeSessionID != "" {
		return true
	}

	// OpenCode sessions with known session ID can always be restarted
	if i.Tool == "opencode" && i.OpenCodeSessionID != "" {
		return true
	}

	// OpenCode sessions without ID can still restart (will start fresh)
	// This allows restart even before session ID is detected
	if i.Tool == "opencode" {
		return true
	}

	// Codex sessions with known session ID can always be restarted
	if i.Tool == "codex" && i.CodexSessionID != "" {
		return true
	}

	// Codex sessions without ID can still restart (will start fresh)
	// This allows restart even before session ID is detected
	if i.Tool == "codex" {
		return true
	}

	// Custom tools: check if they have session resume support
	if i.CanRestartGeneric() {
		return true
	}

	// Other sessions: only if dead or error
	return i.Status == StatusError || i.tmuxSession == nil || !i.tmuxSession.Exists()
}

// CanFork returns true if this session can be forked
func (i *Instance) CanFork() bool {
	// Gemini CLI doesn't support forking
	if i.Tool == "gemini" {
		return false
	}

	// OpenCode sessions can fork if session ID is recent
	if i.Tool == "opencode" {
		return i.CanForkOpenCode()
	}

	// Claude sessions can fork if session ID is recent
	if i.ClaudeSessionID == "" {
		return false
	}
	return time.Since(i.ClaudeDetectedAt) < 5*time.Minute
}

// CanForkOpenCode returns true if this OpenCode session can be forked
func (i *Instance) CanForkOpenCode() bool {
	return i.Tool == "opencode" && i.OpenCodeSessionID != "" && time.Since(i.OpenCodeDetectedAt) < 5*time.Minute
}

// Fork returns the command to create a forked Claude session
// Uses capture-resume pattern: starts fork in print mode to get new session ID,
// stores in tmux environment, then resumes interactively
// Deprecated: Use ForkWithOptions instead
func (i *Instance) Fork(newTitle, newGroupPath string) (string, error) {
	return i.ForkWithOptions(newTitle, newGroupPath, nil)
}

// ForkWithOptions returns the command to create a forked Claude session with custom options
// Uses capture-resume pattern: starts fork in print mode to get new session ID,
// stores in tmux environment, then resumes interactively
func (i *Instance) ForkWithOptions(newTitle, newGroupPath string, opts *ClaudeOptions) (string, error) {
	// Sync session from disk to pick up /clear session changes before forking
	i.syncClaudeSessionFromDisk()

	if !i.CanFork() {
		return "", fmt.Errorf("cannot fork: no active Claude session")
	}

	workDir := i.ProjectPath
	if opts != nil && opts.WorkDir != "" {
		workDir = opts.WorkDir
	}

	// IMPORTANT: For capture-resume commands (which contain $(...) syntax), we MUST use
	// "claude" binary + CLAUDE_CONFIG_DIR, NOT a custom command alias like "cdw".
	// Reason: Commands with $(...) get wrapped in `bash -c` for fish compatibility (#47),
	// and shell aliases are not available in non-interactive bash shells.
	bashExportPrefix := ""
	if IsClaudeConfigDirExplicit() {
		configDir := GetClaudeConfigDir()
		bashExportPrefix = fmt.Sprintf("export CLAUDE_CONFIG_DIR=%s; ", configDir)
	}

	// If no options provided, use defaults from config
	if opts == nil {
		userConfig, _ := LoadUserConfig()
		opts = NewClaudeOptions(userConfig)
	}

	// Build extra flags from options (for fork, we use ToArgsForFork which excludes session mode)
	extraFlags := i.buildClaudeExtraFlags(opts)

	// Pre-generate UUID for forked session and use --session-id flag
	// Note: --session-id works for new/forked sessions as of Claude CLI 2.1.x
	// Note: Path is single-quoted to handle spaces and special characters
	cmd := fmt.Sprintf(
		`cd '%s' && `+
			`session_id=$(uuidgen | tr '[:upper:]' '[:lower:]'); `+
			`tmux set-environment CLAUDE_SESSION_ID "$session_id"; `+
			`%sclaude --session-id "$session_id" --resume %s --fork-session%s`,
		workDir,
		bashExportPrefix, i.ClaudeSessionID, extraFlags)
	cmd, err := i.applyWrapper(cmd)
	if err != nil {
		return "", err
	}

	return cmd, nil
}

// GetActualWorkDir returns the actual working directory from tmux, or falls back to ProjectPath
func (i *Instance) GetActualWorkDir() string {
	if i.tmuxSession != nil {
		if workDir := i.tmuxSession.GetWorkDir(); workDir != "" {
			return workDir
		}
	}
	return i.ProjectPath
}

// CreateForkedInstance creates a new Instance configured for forking
// Deprecated: Use CreateForkedInstanceWithOptions instead
func (i *Instance) CreateForkedInstance(newTitle, newGroupPath string) (*Instance, string, error) {
	return i.CreateForkedInstanceWithOptions(newTitle, newGroupPath, nil)
}

// CreateForkedInstanceWithOptions creates a new Instance configured for forking with custom options
func (i *Instance) CreateForkedInstanceWithOptions(newTitle, newGroupPath string, opts *ClaudeOptions) (*Instance, string, error) {
	cmd, err := i.ForkWithOptions(newTitle, newGroupPath, opts)
	if err != nil {
		return nil, "", err
	}

	// Create new instance - use worktree path if provided, otherwise parent's project path
	projectPath := i.ProjectPath
	if opts != nil && opts.WorkDir != "" {
		projectPath = opts.WorkDir
	}
	forked := NewInstance(newTitle, projectPath)
	if newGroupPath != "" {
		forked.GroupPath = newGroupPath
	} else {
		forked.GroupPath = i.GroupPath
	}
	forked.Command = cmd
	forked.Tool = "claude"

	// Store options in the new instance for persistence
	if opts != nil {
		if err := forked.SetClaudeOptions(opts); err != nil {
			// Log but don't fail - options are not critical for fork
			sessionLog.Warn("set_claude_options_failed", slog.String("error", err.Error()))
		}
		// Copy transient worktree fields to the forked instance
		if opts.WorktreePath != "" {
			forked.WorktreePath = opts.WorktreePath
			forked.WorktreeRepoRoot = opts.WorktreeRepoRoot
			forked.WorktreeBranch = opts.WorktreeBranch
		}
	}

	return forked, cmd, nil
}

// ForkOpenCode returns the command to create a forked OpenCode session.
// Uses export/import to clone the session with a new ID, then launches
// the forked session with opencode -s <new-id>.
// Deprecated: Use ForkOpenCodeWithOptions instead.
func (i *Instance) ForkOpenCode(newTitle, newGroupPath string) (string, error) {
	return i.ForkOpenCodeWithOptions(newTitle, newGroupPath, nil)
}

// ForkOpenCodeWithOptions returns the command to create a forked OpenCode session with custom options.
// Uses export/import to clone the session with a new ID, then launches
// the forked session with opencode -s <new-id> plus any model/agent flags.
func (i *Instance) ForkOpenCodeWithOptions(newTitle, newGroupPath string, opts *OpenCodeOptions) (string, error) {
	if !i.CanForkOpenCode() {
		return "", fmt.Errorf("cannot fork: no active OpenCode session")
	}

	workDir := i.ProjectPath
	envPrefix := i.buildEnvSourceCommand()

	// Build extra flags from options (for fork, exclude session mode flags)
	var extraFlags string
	if opts != nil {
		for _, arg := range opts.ToArgsForFork() {
			extraFlags += " " + arg
		}
	} else if config, err := LoadUserConfig(); err == nil && config != nil {
		defaultOpts := NewOpenCodeOptions(config)
		for _, arg := range defaultOpts.ToArgsForFork() {
			extraFlags += " " + arg
		}
	}

	scriptPath, err := i.writeOpenCodeForkScript(workDir, envPrefix, extraFlags)
	if err != nil {
		return "", fmt.Errorf("failed to create fork script: %w", err)
	}

	return fmt.Sprintf("bash '%s'", scriptPath), nil
}

// writeOpenCodeForkScript writes a bash script that forks via export/import.
// The script self-deletes after execution.
func (i *Instance) writeOpenCodeForkScript(workDir, envPrefix, extraFlags string) (string, error) {
	script := fmt.Sprintf(`#!/bin/bash
cd "%s" || { echo "cd failed to: %s"; exit 1; }
%s
tmpfile=$(mktemp -t opencode-fork)
trap "rm -f \"$tmpfile\" \"$0\"" EXIT

opencode export %s 2>/dev/null > "$tmpfile"
export_status=$?
if [ $export_status -ne 0 ]; then
  echo "Export failed (exit $export_status):"
  cat "$tmpfile"
  exit 1
fi

hash_cmd="md5sum"
command -v md5sum >/dev/null 2>&1 || hash_cmd="md5"
new_id="ses_$(date +%%s | $hash_cmd | head -c12)$(openssl rand -base64 20 | tr -dc a-zA-Z0-9 | head -c14)"
if [[ "$OSTYPE" == "darwin"* ]]; then
  sed -i "" "s/%s/$new_id/g" "$tmpfile" || { echo "Sed failed"; exit 1; }
else
  sed -i "s/%s/$new_id/g" "$tmpfile" || { echo "Sed failed"; exit 1; }
fi
opencode import "$tmpfile" 2>&1 || { echo "Import failed"; exit 1; }
tmux set-environment OPENCODE_SESSION_ID "$new_id"
echo "Forked to: $new_id"
opencode -s "$new_id"%s
`, workDir, workDir, envPrefix, i.OpenCodeSessionID,
		i.OpenCodeSessionID, i.OpenCodeSessionID, extraFlags)

	f, err := os.CreateTemp("", "opencode-fork-*.sh")
	if err != nil {
		return "", err
	}
	defer f.Close()

	if _, err := f.WriteString(script); err != nil {
		os.Remove(f.Name())
		return "", err
	}

	if err := f.Chmod(0755); err != nil {
		os.Remove(f.Name())
		return "", err
	}

	return f.Name(), nil
}

// CreateForkedOpenCodeInstance creates a new Instance configured for forking an OpenCode session
// Deprecated: Use CreateForkedOpenCodeInstanceWithOptions instead.
func (i *Instance) CreateForkedOpenCodeInstance(newTitle, newGroupPath string) (*Instance, string, error) {
	return i.CreateForkedOpenCodeInstanceWithOptions(newTitle, newGroupPath, nil)
}

// CreateForkedOpenCodeInstanceWithOptions creates a new Instance configured for forking with custom options
func (i *Instance) CreateForkedOpenCodeInstanceWithOptions(newTitle, newGroupPath string, opts *OpenCodeOptions) (*Instance, string, error) {
	cmd, err := i.ForkOpenCodeWithOptions(newTitle, newGroupPath, opts)
	if err != nil {
		return nil, "", err
	}

	forked := NewInstance(newTitle, i.ProjectPath)
	if newGroupPath != "" {
		forked.GroupPath = newGroupPath
	} else {
		forked.GroupPath = i.GroupPath
	}
	forked.Command = cmd
	forked.Tool = "opencode"

	// Store options in the new instance for persistence
	if opts != nil {
		if err := forked.SetOpenCodeOptions(opts); err != nil {
			sessionLog.Warn("set_opencode_options_failed", slog.String("error", err.Error()))
		}
	}

	return forked, cmd, nil
}

// Exists checks if the tmux session still exists
func (i *Instance) Exists() bool {
	if i.tmuxSession == nil {
		return false
	}
	return i.tmuxSession.Exists()
}

// GetTmuxSession returns the tmux session object
func (i *Instance) GetTmuxSession() *tmux.Session {
	return i.tmuxSession
}

// SetAcknowledgedFromShared applies an acknowledgment from another TUI instance
// (read from SQLite). This transitions a YELLOW (waiting) session to GRAY (idle)
// without requiring the user to interact with this specific TUI instance.
func (i *Instance) SetAcknowledgedFromShared(ack bool) {
	if !ack || i.tmuxSession == nil {
		return
	}

	// Running/starting is authoritative: don't let stale shared ack force
	// an active session back to idle.
	status := i.GetStatusThreadSafe()
	if status == StatusRunning || status == StatusStarting {
		return
	}

	i.tmuxSession.Acknowledge()
}

// SyncTmuxDisplayName updates the tmux status bar to reflect the current title.
func (i *Instance) SyncTmuxDisplayName() {
	if tmuxSess := i.GetTmuxSession(); tmuxSess != nil && tmuxSess.Exists() {
		tmuxSess.DisplayName = i.Title
		tmuxSess.ConfigureStatusBar()
	}
}

// GetClaudeOptions returns Claude-specific options, or nil if not set
func (i *Instance) GetClaudeOptions() *ClaudeOptions {
	if len(i.ToolOptionsJSON) == 0 {
		return nil
	}
	opts, err := UnmarshalClaudeOptions(i.ToolOptionsJSON)
	if err != nil {
		return nil
	}
	return opts
}

// SetClaudeOptions stores Claude-specific options
func (i *Instance) SetClaudeOptions(opts *ClaudeOptions) error {
	if opts == nil {
		i.ToolOptionsJSON = nil
		return nil
	}
	data, err := MarshalToolOptions(opts)
	if err != nil {
		return err
	}
	i.ToolOptionsJSON = data
	return nil
}

// GetCodexOptions returns Codex-specific options, or nil if not set
func (i *Instance) GetCodexOptions() *CodexOptions {
	if len(i.ToolOptionsJSON) == 0 {
		return nil
	}
	opts, err := UnmarshalCodexOptions(i.ToolOptionsJSON)
	if err != nil {
		return nil
	}
	return opts
}

// SetCodexOptions stores Codex-specific options
func (i *Instance) SetCodexOptions(opts *CodexOptions) error {
	if opts == nil {
		i.ToolOptionsJSON = nil
		return nil
	}
	data, err := MarshalToolOptions(opts)
	if err != nil {
		return err
	}
	i.ToolOptionsJSON = data
	return nil
}

// GetOpenCodeOptions returns OpenCode-specific options, or nil if not set
func (i *Instance) GetOpenCodeOptions() *OpenCodeOptions {
	if len(i.ToolOptionsJSON) == 0 {
		return nil
	}
	opts, err := UnmarshalOpenCodeOptions(i.ToolOptionsJSON)
	if err != nil {
		return nil
	}
	return opts
}

// SetOpenCodeOptions stores OpenCode-specific options
func (i *Instance) SetOpenCodeOptions(opts *OpenCodeOptions) error {
	if opts == nil {
		i.ToolOptionsJSON = nil
		return nil
	}
	data, err := MarshalToolOptions(opts)
	if err != nil {
		return err
	}
	i.ToolOptionsJSON = data
	return nil
}

// GetSessionIDFromTmux reads Claude session ID from tmux environment
// This is the primary method for sessions started with the capture-resume pattern
func (i *Instance) GetSessionIDFromTmux() string {
	if i.tmuxSession == nil {
		return ""
	}
	sessionID, err := i.tmuxSession.GetEnvironment("CLAUDE_SESSION_ID")
	if err != nil {
		return ""
	}
	return sessionID
}

// GetMCPInfo returns MCP server information for this session
// Returns nil if not a Claude or Gemini session
func (i *Instance) GetMCPInfo() *MCPInfo {
	switch i.Tool {
	case "claude":
		return GetMCPInfo(i.ProjectPath)
	case "gemini":
		return GetGeminiMCPInfo(i.ProjectPath)
	default:
		return nil
	}
}

// CaptureLoadedMCPs captures the current MCP names as the "loaded" state
// This should be called when a session starts or restarts, so we can track
// which MCPs are actually loaded in the running Claude session vs just configured
func (i *Instance) CaptureLoadedMCPs() {
	if i.Tool != "claude" {
		i.LoadedMCPNames = nil
		return
	}

	mcpInfo := GetMCPInfo(i.ProjectPath)
	if mcpInfo == nil {
		i.LoadedMCPNames = nil
		return
	}

	i.LoadedMCPNames = mcpInfo.AllNames()
}

// regenerateMCPConfig regenerates .mcp.json with current pool status
// If socket pool is running, MCPs will use socket configs (nc -U /tmp/...)
// Otherwise, MCPs will use stdio configs (npx ...)
// Returns error if .mcp.json write fails
func (i *Instance) regenerateMCPConfig() error {
	ClearMCPCache(i.ProjectPath) // Force fresh read from disk (not stale 30s cache)
	mcpInfo := GetMCPInfo(i.ProjectPath)
	if mcpInfo == nil {
		return nil // No MCP info, nothing to regenerate
	}

	switch GetMCPDefaultScope() {
	case "global":
		globalMCPs := mcpInfo.Global
		if len(globalMCPs) == 0 {
			return nil
		}
		if err := WriteGlobalMCP(globalMCPs); err != nil {
			mcpLog.Debug("regen_global_mcp_failed", slog.String("error", err.Error()))
			return fmt.Errorf("failed to regenerate global MCP config: %w", err)
		}
		mcpLog.Debug("regen_global_mcp_succeeded", slog.String("title", i.Title), slog.Int("mcp_count", len(globalMCPs)))
	case "user":
		userMCPs := GetUserMCPNames()
		if len(userMCPs) == 0 {
			return nil
		}
		if err := WriteUserMCP(userMCPs); err != nil {
			mcpLog.Debug("regen_user_mcp_failed", slog.String("error", err.Error()))
			return fmt.Errorf("failed to regenerate user MCP config: %w", err)
		}
		mcpLog.Debug("regen_user_mcp_succeeded", slog.String("title", i.Title), slog.Int("mcp_count", len(userMCPs)))
	default:
		localMCPs := mcpInfo.Local()
		if len(localMCPs) == 0 {
			return nil
		}
		// WriteMCPJsonFromConfig checks pool status and writes socket configs if pool is running
		if err := WriteMCPJsonFromConfig(i.ProjectPath, localMCPs); err != nil {
			mcpLog.Debug("regen_local_mcp_failed", slog.String("error", err.Error()))
			return fmt.Errorf("failed to regenerate .mcp.json: %w", err)
		}
		mcpLog.Debug("regen_local_mcp_succeeded", slog.String("title", i.Title), slog.Int("mcp_count", len(localMCPs)))
	}
	return nil
}

// sessionHasConversationData checks if a Claude session file contains actual
// conversation data (has "sessionId" field in records).
//
// Returns true if:
// - File has any "sessionId" field (user interacted with session)
// - Error reading file (safe fallback - don't risk losing sessions)
//
// Returns false if:
// - File doesn't exist (nothing to resume, use --session-id)
// - File exists but has zero "sessionId" occurrences (never interacted)
func sessionHasConversationData(sessionID string, projectPath string) bool {
	// Build the session file path
	// Format: {config_dir}/projects/{encoded_path}/{sessionID}.jsonl
	configDir := GetClaudeConfigDir()
	if configDir == "" {
		configDir = filepath.Join(os.Getenv("HOME"), ".claude")
	}

	// Resolve symlinks in project path (macOS: /tmp -> /private/tmp)
	resolvedPath := projectPath
	if resolved, err := filepath.EvalSymlinks(projectPath); err == nil {
		resolvedPath = resolved
	}

	// Encode project path using Claude's directory format
	encodedPath := ConvertToClaudeDirName(resolvedPath)
	if encodedPath == "" {
		encodedPath = "-"
	}

	sessionFile := filepath.Join(configDir, "projects", encodedPath, sessionID+".jsonl")
	sessionLog.Debug("session_data_checking_file", slog.String("file", sessionFile))

	// Check if file exists
	if _, err := os.Stat(sessionFile); os.IsNotExist(err) {
		// File doesn't exist at expected location - try cross-project search
		// This handles path hash mismatches (e.g., session created from different directory)
		if fallbackPath := findSessionFileInAllProjects(sessionID); fallbackPath != "" {
			sessionLog.Debug("session_data_cross_project_found", slog.String("path", fallbackPath))
			sessionFile = fallbackPath
		} else {
			// File doesn't exist anywhere - use --session-id to create fresh session
			// (there's nothing to resume if the file doesn't exist)
			sessionLog.Debug("session_data_file_not_found", slog.String("result", "use_session_id"))
			return false
		}
	}

	sessionLog.Debug("session_data_file_exists", slog.String("file", sessionFile))

	// Read file and search for "sessionId" field
	file, err := os.Open(sessionFile)
	if err != nil {
		// Error opening - safe fallback to --resume
		sessionLog.Debug("session_data_open_error", slog.String("error", err.Error()), slog.String("fallback", "use_resume"))
		return true
	}
	defer file.Close()

	// Use scanner to read line by line (memory efficient for large files)
	scanner := bufio.NewScanner(file)
	// Increase buffer size for long lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		// Simple string search - faster than JSON parsing
		if strings.Contains(line, `"sessionId"`) {
			sessionLog.Debug("session_data_found_session_id", slog.String("result", "use_resume"))
			return true // Found conversation data
		}
	}

	if err := scanner.Err(); err != nil {
		// Error reading - safe fallback to --resume
		sessionLog.Debug("session_data_scanner_error", slog.String("error", err.Error()), slog.String("fallback", "use_resume"))
		return true
	}

	// No sessionId found - session was never interacted with
	sessionLog.Debug("session_data_no_session_id", slog.String("result", "use_session_id"))
	return false
}

// findSessionFileInAllProjects searches all Claude project directories for a session file
// This handles path hash mismatches when agent-deck runs from a different directory
// than where the Claude session was originally created.
// Returns the full path to the session file, or empty string if not found.
func findSessionFileInAllProjects(sessionID string) string {
	if sessionID == "" {
		return ""
	}

	configDir := GetClaudeConfigDir()
	if configDir == "" {
		configDir = filepath.Join(os.Getenv("HOME"), ".claude")
	}

	projectsDir := filepath.Join(configDir, "projects")

	// List all project hash directories
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return ""
	}

	// Session filename format: {sessionID}.jsonl
	sessionFile := sessionID + ".jsonl"

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		filePath := filepath.Join(projectsDir, entry.Name(), sessionFile)
		if _, err := os.Stat(filePath); err == nil {
			return filePath
		}
	}

	return ""
}

// generateID generates a unique session ID
func generateID() string {
	return fmt.Sprintf("%s-%d", randomString(8), time.Now().Unix())
}

// randomString generates a random hex string of specified length
func randomString(length int) string {
	bytes := make([]byte, length/2)
	if _, err := rand.Read(bytes); err != nil {
		// Fallback to timestamp-based ID
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(bytes)
}

// UpdateClaudeSessionsWithDedup clears duplicate Claude session IDs across instances.
// The oldest session (by CreatedAt) keeps its ID, newer duplicates are cleared.
// With tmux env being authoritative, duplicates shouldn't occur in normal use,
// but we handle them defensively for loaded/migrated sessions.
func UpdateClaudeSessionsWithDedup(instances []*Instance) {
	// Sort instances by CreatedAt (older first get priority for keeping IDs)
	sort.Slice(instances, func(i, j int) bool {
		return instances[i].CreatedAt.Before(instances[j].CreatedAt)
	})

	// Find and clear duplicate IDs (keep only the oldest session's claim)
	idOwner := make(map[string]*Instance)
	for _, inst := range instances {
		if inst.Tool != "claude" || inst.ClaudeSessionID == "" {
			continue
		}
		if owner, exists := idOwner[inst.ClaudeSessionID]; exists {
			// Duplicate found! The older session (owner) keeps the ID
			// Clear the newer session's ID (it will get a new one from tmux env)
			inst.ClaudeSessionID = ""
			inst.ClaudeDetectedAt = time.Time{}
			_ = owner // Older session keeps its ID
		} else {
			idOwner[inst.ClaudeSessionID] = inst
		}
	}
	// No re-detection step - tmux env is the authoritative source
	// Sessions will get their IDs from UpdateClaudeSession() during normal status updates
}
