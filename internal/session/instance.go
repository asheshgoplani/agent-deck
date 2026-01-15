package session

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Status represents the current state of a session
type Status string

const (
	StatusRunning  Status = "running"
	StatusWaiting  Status = "waiting"
	StatusIdle     Status = "idle"
	StatusError    Status = "error"
	StatusStarting Status = "starting" // Session is being created (tmux initializing)
)

// Instance represents a single agent/shell session
type Instance struct {
	ID             string    `json:"id"`
	Title          string    `json:"title"`
	ProjectPath    string    `json:"project_path"`
	GroupPath      string    `json:"group_path"` // e.g., "projects/devops"
	ParentSessionID string   `json:"parent_session_id,omitempty"` // Links to parent session (makes this a sub-session)

	// Git worktree support
	WorktreePath     string `json:"worktree_path,omitempty"`      // Path to worktree (if session is in worktree)
	WorktreeRepoRoot string `json:"worktree_repo_root,omitempty"` // Original repo root
	WorktreeBranch   string `json:"worktree_branch,omitempty"`    // Branch name in worktree

	Command        string    `json:"command"`
	Tool           string    `json:"tool"`
	Status         Status    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	LastAccessedAt time.Time `json:"last_accessed_at,omitempty"` // When user last attached

	// Claude Code integration
	ClaudeSessionID  string    `json:"claude_session_id,omitempty"`
	ClaudeDetectedAt time.Time `json:"claude_detected_at,omitempty"`

	// Gemini CLI integration
	GeminiSessionID  string    `json:"gemini_session_id,omitempty"`
	GeminiDetectedAt time.Time `json:"gemini_detected_at,omitempty"`

	// MCP tracking - which MCPs were loaded when session started/restarted
	// Used to detect pending MCPs (added after session start) and stale MCPs (removed but still running)
	LoadedMCPNames []string `json:"loaded_mcp_names,omitempty"`

	tmuxSession *tmux.Session // Internal tmux session

	// lastErrorCheck tracks when we last confirmed the session doesn't exist
	// Used to skip expensive Exists() checks for ghost sessions (sessions in JSON but not in tmux)
	// Not serialized - resets on load, but that's fine since we'll recheck on first poll
	lastErrorCheck time.Time

	// lastStartTime tracks when Start() was called
	// Used to provide grace period for tmux session creation (prevents error flash)
	// Not serialized - only relevant for current TUI session
	lastStartTime time.Time

	// SkipMCPRegenerate skips .mcp.json regeneration on next Restart()
	// Set by MCP dialog Apply() to avoid race condition where Apply writes
	// config then Restart immediately overwrites it with different pool state
	SkipMCPRegenerate bool `json:"-"` // Don't persist, transient flag
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

// ClearParent removes the parent session link
func (inst *Instance) ClearParent() {
	inst.ParentSessionID = ""
}

// NewInstance creates a new session instance
func NewInstance(title, projectPath string) *Instance {
	return &Instance{
		ID:          generateID(),
		Title:       title,
		ProjectPath: projectPath,
		GroupPath:   extractGroupPath(projectPath), // Auto-assign group from path
		Tool:        "shell",
		Status:      StatusIdle,
		CreatedAt:   time.Now(),
		tmuxSession: tmux.NewSession(title, projectPath),
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
	inst := &Instance{
		ID:          generateID(),
		Title:       title,
		ProjectPath: projectPath,
		GroupPath:   extractGroupPath(projectPath),
		Tool:        tool,
		Status:      StatusIdle,
		CreatedAt:   time.Now(),
		tmuxSession: tmux.NewSession(title, projectPath),
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
// Respects: CLAUDE_CONFIG_DIR, dangerous_mode from user config
func (i *Instance) buildClaudeCommand(baseCommand string) string {
	return i.buildClaudeCommandWithMessage(baseCommand, "")
}

// buildClaudeCommandWithMessage builds the command with optional initial message
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

	// Check if dangerous mode is enabled in user config
	dangerousMode := false
	if userConfig, err := LoadUserConfig(); err == nil && userConfig != nil {
		dangerousMode = userConfig.Claude.DangerousMode
	}

	// If baseCommand is just "claude", build the capture-resume command
	// This command:
	// 1. Runs Claude with a minimal prompt "." to completion (creates session)
	// 2. Extracts session_id from the JSON output
	// 3. Stores session ID in tmux environment (for retrieval by agent-deck)
	// 4. Resumes that session interactively
	// NOTE: We cannot use --session-id for NEW sessions - Claude ignores it and creates its own ID.
	// The --session-id flag only works for RESUMING existing sessions.
	// Fallback: If capture fails (jq not installed, etc.), start Claude fresh
	if baseCommand == "claude" {
		dangerousFlag := ""
		if dangerousMode {
			dangerousFlag = " --dangerously-skip-permissions"
		}

		// Build the capture-resume command
		// Uses --output-format json to get session ID, then resumes
		baseCmd := fmt.Sprintf(
			`session_id=$(%s%s -p "." --output-format json 2>/dev/null | jq -r 'if type == "array" then .[0].session_id else .session_id end' 2>/dev/null) || session_id=""; `+
				`if [ -n "$session_id" ] && [ "$session_id" != "null" ]; then `+
				`tmux set-environment CLAUDE_SESSION_ID "$session_id"; `+
				`%s%s --resume "$session_id"%s; `+
				`else %s%s%s; fi`,
			configDirPrefix, claudeCmd,
			configDirPrefix, claudeCmd, dangerousFlag,
			configDirPrefix, claudeCmd, dangerousFlag)

		// If message provided, append wait-and-send logic
		if message != "" {
			// Escape single quotes in message for bash
			escapedMsg := strings.ReplaceAll(message, "'", "'\"'\"'")

			// Capture-resume with wait-and-send in background
			// The wait loop runs in a subshell that polls for ">" prompt (Claude's input prompt)
			// Once detected, sends the message via tmux send-keys (text + Enter separately)
			baseCmd = fmt.Sprintf(
				`session_id=$(%s%s -p "." --output-format json 2>/dev/null | jq -r 'if type == "array" then .[0].session_id else .session_id end' 2>/dev/null) || session_id=""; `+
					`if [ -n "$session_id" ] && [ "$session_id" != "null" ]; then `+
					`tmux set-environment CLAUDE_SESSION_ID "$session_id"; `+
					`(sleep 2; SESSION_NAME=$(tmux display-message -p '#S'); `+
					`while ! tmux capture-pane -p -t "$SESSION_NAME" | tail -5 | grep -qE "^>"; do sleep 0.2; done; `+
					`tmux send-keys -l -t "$SESSION_NAME" '%s'; tmux send-keys -t "$SESSION_NAME" Enter) & `+
					`%s%s --resume "$session_id"%s; `+
					`else %s%s%s; fi`,
				configDirPrefix, claudeCmd,
				escapedMsg,
				configDirPrefix, claudeCmd, dangerousFlag,
				configDirPrefix, claudeCmd, dangerousFlag)
		}

		return baseCmd
	}

	// For custom commands (e.g., fork commands), return as-is
	return baseCommand
}

// buildGeminiCommand builds the gemini command with session capture
// For new sessions: captures session ID via stream-json, stores in tmux env, then resumes
// For sessions with known ID: uses simple resume
// This ensures we always know the session ID for restart features
// VERIFIED: gemini --output-format stream-json provides immediate session ID in first message
func (i *Instance) buildGeminiCommand(baseCommand string) string {
	if i.Tool != "gemini" {
		return baseCommand
	}

	// If baseCommand is just "gemini", handle specially
	if baseCommand == "gemini" {
		// If we already have a session ID, use simple resume
		if i.GeminiSessionID != "" {
			return fmt.Sprintf("gemini --resume %s", i.GeminiSessionID)
		}

		// Build the capture-resume command for new sessions with fallback
		// This command:
		// 1. Runs Gemini with a minimal prompt "." to completion (saves session to disk)
		// 2. Extracts session_id from the JSON output
		// 3. Stores session ID in tmux environment (for retrieval by agent-deck)
		// 4. Resumes that session interactively
		// Fallback: If capture fails (jq not installed, auth issues), start Gemini fresh
		// NOTE: Using --output-format json (not stream-json with head -1) because:
		// - head -1 sends SIGPIPE which kills Gemini before it saves the session
		// - json mode runs to completion, ensuring session file is written
		return `session_id=$(gemini --output-format json "." 2>/dev/null | jq -r 'if type == "array" then .[0].session_id else .session_id end' 2>/dev/null) || session_id=""; ` +
			`if [ -n "$session_id" ] && [ "$session_id" != "null" ]; then ` +
			`tmux set-environment GEMINI_SESSION_ID "$session_id"; ` +
			`gemini --resume "$session_id"; ` +
			`else gemini; fi`
	}

	// For custom commands (e.g., resume commands), return as-is
	return baseCommand
}

// buildGenericCommand builds command for user-defined tools from config.toml
// If the tool has session resume config, builds capture-resume command similar to Claude/Gemini
// Otherwise returns the base command as-is
//
// Config fields used:
//   - resume_flag: CLI flag to resume (e.g., "--resume")
//   - session_id_env: tmux env var name (e.g., "VIBE_SESSION_ID")
//   - session_id_json_path: jq path to extract ID (e.g., ".session_id")
//   - output_format_flag: flag to get JSON output (e.g., "--output-format json")
//   - dangerous_flag: flag to skip confirmations (e.g., "--auto-approve")
//   - dangerous_mode: whether to enable dangerous flag by default
func (i *Instance) buildGenericCommand(baseCommand string) string {
	toolDef := GetToolDef(i.Tool)
	if toolDef == nil {
		return baseCommand // No custom config, return as-is
	}

	// Check if tool supports session resume (needs both resume_flag and session_id_env)
	if toolDef.ResumeFlag == "" || toolDef.SessionIDEnv == "" {
		// No session resume support, just add dangerous flag if configured
		if toolDef.DangerousMode && toolDef.DangerousFlag != "" {
			return fmt.Sprintf("%s %s", baseCommand, toolDef.DangerousFlag)
		}
		return baseCommand
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
		return fmt.Sprintf("tmux set-environment %s %s && %s %s %s%s",
			toolDef.SessionIDEnv, existingSessionID,
			baseCommand, toolDef.ResumeFlag, existingSessionID, dangerousFlag)
	}

	// No existing session ID - need to capture it on first run
	// This requires output_format_flag and session_id_json_path
	if toolDef.OutputFormatFlag == "" || toolDef.SessionIDJsonPath == "" {
		// Can't capture session ID, just start normally
		if dangerousFlag != "" {
			return baseCommand + dangerousFlag
		}
		return baseCommand
	}

	// Build capture-resume command similar to Claude/Gemini
	// Pattern:
	// 1. Run tool with minimal prompt to get session ID
	// 2. Extract ID using jq
	// 3. Store in tmux environment
	// 4. Resume that session
	// Fallback: If capture fails, start tool fresh
	return fmt.Sprintf(
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

// loadCustomPatternsFromConfig loads custom detection patterns from config.toml
// and sets them on the tmux session for status detection and tool auto-detection
func (i *Instance) loadCustomPatternsFromConfig() {
	if i.tmuxSession == nil {
		return
	}

	toolDef := GetToolDef(i.Tool)
	if toolDef == nil {
		return // No custom config for this tool
	}

	// Set custom patterns on the tmux session
	// The tool name is passed so DetectTool() knows what to return when patterns match
	i.tmuxSession.SetCustomPatterns(
		i.Tool, // Tool name (e.g., "vibe", "aider")
		toolDef.BusyPatterns,
		toolDef.PromptPatterns,
		toolDef.DetectPatterns,
	)
}

// Start starts the session in tmux
func (i *Instance) Start() error {
	if i.tmuxSession == nil {
		return fmt.Errorf("tmux session not initialized")
	}

	// Build command based on tool type
	// Priority: built-in tools (claude, gemini) → custom tools from config.toml → raw command
	var command string
	switch i.Tool {
	case "claude":
		command = i.buildClaudeCommand(i.Command)
	case "gemini":
		command = i.buildGeminiCommand(i.Command)
	default:
		// Check if this is a custom tool with session resume config
		if toolDef := GetToolDef(i.Tool); toolDef != nil {
			command = i.buildGenericCommand(i.Command)
		} else {
			command = i.Command
		}
	}

	// Load custom patterns for status detection
	i.loadCustomPatternsFromConfig()

	// Start the tmux session
	if err := i.tmuxSession.Start(command); err != nil {
		return fmt.Errorf("failed to start tmux session: %w", err)
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
	// Priority: built-in tools (claude, gemini) → custom tools from config.toml → raw command
	var command string
	switch i.Tool {
	case "claude":
		command = i.buildClaudeCommand(i.Command)
	case "gemini":
		command = i.buildGeminiCommand(i.Command)
	default:
		// Check if this is a custom tool with session resume config
		if toolDef := GetToolDef(i.Tool); toolDef != nil {
			command = i.buildGenericCommand(i.Command)
		} else {
			command = i.Command
		}
	}

	// Load custom patterns for status detection
	i.loadCustomPatternsFromConfig()

	// Start the tmux session
	if err := i.tmuxSession.Start(command); err != nil {
		return fmt.Errorf("failed to start tmux session: %w", err)
	}

	// Capture MCPs that are now loaded (for sync tracking)
	i.CaptureLoadedMCPs()

	// Record start time for grace period (prevents error flash during tmux startup)
	i.lastStartTime = time.Now()

	// New sessions start as STARTING
	i.Status = StatusStarting

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

	sessionName := i.tmuxSession.Name

	// Track state transitions: we need to see "active" before accepting "waiting"
	// This ensures we don't send the message during initial startup (false "waiting")
	sawActive := false
	waitingCount := 0 // Track consecutive "waiting" states to detect already-ready sessions
	maxAttempts := 300 // 60 seconds max (300 * 200ms) - Claude with MCPs can take 40-60s

	for attempt := 0; attempt < maxAttempts; attempt++ {
		time.Sleep(200 * time.Millisecond)

		// Use the existing robust status detection
		status, err := i.tmuxSession.GetStatus()
		if err != nil {
			waitingCount = 0 // Reset on error
			continue
		}

		if status == "active" {
			sawActive = true
			waitingCount = 0
			continue
		}

		if status == "waiting" {
			waitingCount++
		} else {
			waitingCount = 0
		}

		// Agent is ready when either:
		// 1. We've seen "active" (loading) and now see "waiting" (ready)
		// 2. We've seen "waiting" 10+ times consecutively (already processed initial ".")
		//    This handles the race where Claude finishes before we start checking
		alreadyReady := waitingCount >= 10 && attempt >= 15 // At least 3s elapsed
		if (sawActive && status == "waiting") || alreadyReady {
			// Small delay to ensure UI is fully rendered
			time.Sleep(300 * time.Millisecond)

			// Send the message using tmux send-keys
			// -l flag for literal text, then Enter separately
			cmd := exec.Command("tmux", "send-keys", "-l", "-t", sessionName, message)
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("failed to send message: %w", err)
			}

			cmd = exec.Command("tmux", "send-keys", "-t", sessionName, "Enter")
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("failed to send Enter: %w", err)
			}

			return nil
		}
	}

	return fmt.Errorf("timeout waiting for agent to be ready")
}

// errorRecheckInterval - how often to recheck sessions that don't exist
// Ghost sessions (in JSON but not in tmux) are rechecked at this interval
// instead of every 500ms tick, dramatically reducing subprocess spawns
const errorRecheckInterval = 30 * time.Second

// UpdateStatus updates the session status by checking tmux
func (i *Instance) UpdateStatus() error {
	// Grace period FIRST: Skip all checks for recently created sessions
	// If session was created within last 5 seconds, keep status as starting
	// This prevents error flash during auto-reload while tmux initializes
	if time.Since(i.CreatedAt) < 5*time.Second {
		// Keep status as starting during grace period
		if i.Status != StatusRunning && i.Status != StatusIdle {
			i.Status = StatusStarting
		}
		return nil
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

	// Get status from tmux session
	status, err := i.tmuxSession.GetStatus()
	if err != nil {
		i.Status = StatusError
		return err
	}

	// Map tmux status to instance status
	switch status {
	case "active":
		i.Status = StatusRunning
	case "waiting":
		i.Status = StatusWaiting
	case "idle":
		i.Status = StatusIdle
	default:
		i.Status = StatusError
	}

	// Update tool detection dynamically (enables fork when Claude starts)
	if detectedTool := i.tmuxSession.DetectTool(); detectedTool != "" {
		i.Tool = detectedTool
	}

	// Update Claude session tracking (non-blocking, best-effort)
	// Pass nil for excludeIDs - deduplication happens at manager level
	i.UpdateClaudeSession(nil)

	// Update Gemini session tracking (non-blocking, best-effort)
	if i.Tool == "gemini" {
		i.UpdateGeminiSession(nil)
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
			i.ClaudeSessionID = sessionID
		}
		i.ClaudeDetectedAt = time.Now()
	}
}

// UpdateGeminiSession updates the Gemini session ID from tmux environment.
// The capture-resume pattern (used in Start/Restart) sets GEMINI_SESSION_ID
// in the tmux environment, making this the single authoritative source.
//
// No file scanning fallback - we rely on the consistent capture-resume pattern.
func (i *Instance) UpdateGeminiSession(excludeIDs map[string]bool) {
	if i.Tool != "gemini" {
		return
	}

	// Read from tmux environment (set by capture-resume pattern)
	if i.tmuxSession != nil {
		if sessionID, err := i.tmuxSession.GetEnvironment("GEMINI_SESSION_ID"); err == nil && sessionID != "" {
			if i.GeminiSessionID != sessionID {
				i.GeminiSessionID = sessionID
			}
			i.GeminiDetectedAt = time.Now()
		}
	}
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
	// /Users/ashesh/claude-deck -> -Users-ashesh-claude-deck
	projectDirName := strings.ReplaceAll(resolvedPath, "/", "-")
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
	// /Users/ashesh/claude-deck -> -Users-ashesh-claude-deck
	projectDirName := strings.ReplaceAll(resolvedPath, "/", "-")
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
			ID        string          `json:"id"`
			Timestamp string          `json:"timestamp"`
			Type      string          `json:"type"` // VERIFIED: "user" or "gemini"
			Content   string          `json:"content"`
			ToolCalls []json.RawMessage `json:"toolCalls,omitempty"`
			Thoughts  []json.RawMessage `json:"thoughts,omitempty"`
			Model     string          `json:"model,omitempty"`
			Tokens    json.RawMessage `json:"tokens,omitempty"`
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
	return nil
}

// Restart restarts the Claude session
// For Claude sessions with known ID: sends Ctrl+C twice and resume command to existing session
// For dead sessions or unknown ID: recreates the tmux session
func (i *Instance) Restart() error {
	log.Printf("[MCP-DEBUG] Instance.Restart() called - Tool=%s, ClaudeSessionID=%q, tmuxSession=%v, tmuxExists=%v",
		i.Tool, i.ClaudeSessionID, i.tmuxSession != nil, i.tmuxSession != nil && i.tmuxSession.Exists())

	// Clear flag immediately to prevent it staying set if restart fails
	skipRegen := i.SkipMCPRegenerate
	i.SkipMCPRegenerate = false

	// Regenerate .mcp.json before restart to use socket pool if available
	// Skip if MCP dialog just wrote the config (avoids race condition)
	if i.Tool == "claude" && !skipRegen {
		if err := i.regenerateMCPConfig(); err != nil {
			log.Printf("[MCP-DEBUG] Warning: MCP config regeneration failed: %v", err)
			// Continue with restart - Claude will use existing .mcp.json or defaults
		}
	} else if skipRegen {
		log.Printf("[MCP-DEBUG] Skipping MCP regeneration (flag set by Apply)")
	}

	// If Claude session with known ID AND tmux session exists, use respawn-pane
	if i.Tool == "claude" && i.ClaudeSessionID != "" && i.tmuxSession != nil && i.tmuxSession.Exists() {
		// Build the resume command with proper config
		resumeCmd := i.buildClaudeResumeCommand()
		log.Printf("[MCP-DEBUG] Using respawn-pane with command: %s", resumeCmd)

		// Use respawn-pane for atomic restart
		// This is more reliable than Ctrl+C + wait for shell + send command
		// respawn-pane -k kills the current process and starts the new command atomically
		if err := i.tmuxSession.RespawnPane(resumeCmd); err != nil {
			log.Printf("[MCP-DEBUG] RespawnPane failed: %v", err)
			return fmt.Errorf("failed to restart Claude session: %w", err)
		}

		log.Printf("[MCP-DEBUG] RespawnPane succeeded")

		// Re-capture MCPs after restart (they may have changed since session started)
		i.CaptureLoadedMCPs()

		// Start as WAITING - will go GREEN on next tick if Claude shows busy indicator
		i.Status = StatusWaiting
		return nil
	}

	// If Gemini session with known ID AND tmux session exists, use respawn-pane
	if i.Tool == "gemini" && i.GeminiSessionID != "" && i.tmuxSession != nil && i.tmuxSession.Exists() {
		// Build Gemini resume command with tmux env update
		resumeCmd := fmt.Sprintf("tmux set-environment GEMINI_SESSION_ID %s && gemini --resume %s",
			i.GeminiSessionID, i.GeminiSessionID)
		log.Printf("[RESTART-DEBUG] Gemini using respawn-pane with command: %s", resumeCmd)

		if err := i.tmuxSession.RespawnPane(resumeCmd); err != nil {
			log.Printf("[RESTART-DEBUG] Gemini RespawnPane failed: %v", err)
			return fmt.Errorf("failed to restart Gemini session: %w", err)
		}

		log.Printf("[RESTART-DEBUG] Gemini RespawnPane succeeded")
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

		log.Printf("[RESTART-DEBUG] Generic tool '%s' using respawn-pane with command: %s", i.Tool, resumeCmd)

		if err := i.tmuxSession.RespawnPane(resumeCmd); err != nil {
			log.Printf("[RESTART-DEBUG] Generic tool RespawnPane failed: %v", err)
			return fmt.Errorf("failed to restart %s session: %w", i.Tool, err)
		}

		log.Printf("[RESTART-DEBUG] Generic tool RespawnPane succeeded")
		i.loadCustomPatternsFromConfig() // Reload custom patterns
		i.Status = StatusWaiting
		return nil
	}

	log.Printf("[MCP-DEBUG] Using fallback: recreate tmux session")

	// Fallback: recreate tmux session (for dead sessions or unknown ID)
	i.tmuxSession = tmux.NewSession(i.Title, i.ProjectPath)

	var command string
	if i.Tool == "claude" && i.ClaudeSessionID != "" {
		command = i.buildClaudeResumeCommand()
	} else if i.Tool == "gemini" && i.GeminiSessionID != "" {
		// Set GEMINI_SESSION_ID in tmux env so detection works after restart
		command = fmt.Sprintf("tmux set-environment GEMINI_SESSION_ID %s && gemini --resume %s",
			i.GeminiSessionID, i.GeminiSessionID)
	} else {
		// Route to appropriate command builder based on tool
		switch i.Tool {
		case "claude":
			command = i.buildClaudeCommand(i.Command)
		case "gemini":
			command = i.buildGeminiCommand(i.Command)
		default:
			// Check if this is a custom tool with session resume config
			if toolDef := GetToolDef(i.Tool); toolDef != nil {
				command = i.buildGenericCommand(i.Command)
			} else {
				command = i.Command
			}
		}
	}

	// Load custom patterns for status detection (for custom tools)
	i.loadCustomPatternsFromConfig()

	log.Printf("[MCP-DEBUG] Starting new tmux session with command: %s", command)

	if err := i.tmuxSession.Start(command); err != nil {
		log.Printf("[MCP-DEBUG] tmuxSession.Start() failed: %v", err)
		i.Status = StatusError
		return fmt.Errorf("failed to restart tmux session: %w", err)
	}

	log.Printf("[MCP-DEBUG] tmuxSession.Start() succeeded")

	// Re-capture MCPs after restart
	i.CaptureLoadedMCPs()

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

	// Check if dangerous mode is enabled in user config
	dangerousMode := false
	if userConfig, err := LoadUserConfig(); err == nil && userConfig != nil {
		dangerousMode = userConfig.Claude.DangerousMode
	}

	// Build the command with tmux environment update
	// This ensures CLAUDE_SESSION_ID is set in tmux env after restart,
	// so GetSessionIDFromTmux() works correctly and detects the session
	if dangerousMode {
		return fmt.Sprintf("tmux set-environment CLAUDE_SESSION_ID %s && %s%s --resume %s --dangerously-skip-permissions",
			i.ClaudeSessionID, configDirPrefix, claudeCmd, i.ClaudeSessionID)
	}
	return fmt.Sprintf("tmux set-environment CLAUDE_SESSION_ID %s && %s%s --resume %s",
		i.ClaudeSessionID, configDirPrefix, claudeCmd, i.ClaudeSessionID)
}

// CanRestart returns true if the session can be restarted
// For Claude sessions with known ID: can always restart (interrupt and resume)
// For Gemini sessions with known ID: can always restart (interrupt and resume)
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

	// Claude sessions can fork if session ID is recent
	if i.ClaudeSessionID == "" {
		return false
	}
	return time.Since(i.ClaudeDetectedAt) < 5*time.Minute
}

// Fork returns the command to create a forked Claude session
// Uses capture-resume pattern: starts fork in print mode to get new session ID,
// stores in tmux environment, then resumes interactively
func (i *Instance) Fork(newTitle, newGroupPath string) (string, error) {
	if !i.CanFork() {
		return "", fmt.Errorf("cannot fork: no active Claude session")
	}

	workDir := i.ProjectPath

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

	// Check dangerous mode from user config (same logic as buildClaudeResumeCommand)
	dangerousMode := false
	if userConfig, err := LoadUserConfig(); err == nil && userConfig != nil {
		dangerousMode = userConfig.Claude.DangerousMode
	}

	// Build dangerous mode flag
	dangerousFlag := ""
	if dangerousMode {
		dangerousFlag = " --dangerously-skip-permissions"
	}

	// Capture-resume pattern for fork:
	// 1. Fork in print mode to get new session ID
	// 2. Store in tmux environment (if capture succeeded)
	// 3. Resume the forked session interactively
	// Note: Path is single-quoted to handle spaces and special characters
	// Note: We add jq stderr suppression and validation, but fail if capture fails entirely
	cmd := fmt.Sprintf(
		`cd '%s' && session_id=$(%s%s -p "." --output-format json --resume %s --fork-session 2>/dev/null | jq -r 'if type == "array" then .[0].session_id else .session_id end' 2>/dev/null); `+
			`if [ -z "$session_id" ] || [ "$session_id" = "null" ]; then echo "Fork failed: could not capture session ID. Check if Claude CLI is authenticated and jq is installed."; exit 1; fi; `+
			`tmux set-environment CLAUDE_SESSION_ID "$session_id" && `+
			`%s%s --resume "$session_id"%s`,
		workDir, configDirPrefix, claudeCmd, i.ClaudeSessionID, configDirPrefix, claudeCmd, dangerousFlag)

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
func (i *Instance) CreateForkedInstance(newTitle, newGroupPath string) (*Instance, string, error) {
	cmd, err := i.Fork(newTitle, newGroupPath)
	if err != nil {
		return nil, "", err
	}

	// Create new instance with the PARENT's project path
	// This ensures the forked session is in the same Claude project directory as parent
	forked := NewInstance(newTitle, i.ProjectPath)
	if newGroupPath != "" {
		forked.GroupPath = newGroupPath
	} else {
		forked.GroupPath = i.GroupPath
	}
	forked.Command = cmd
	forked.Tool = "claude"

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
	mcpInfo := GetMCPInfo(i.ProjectPath)
	if mcpInfo == nil {
		return nil // No MCP info, nothing to regenerate
	}

	localMCPs := mcpInfo.Local()
	if len(localMCPs) == 0 {
		return nil // No local MCPs, nothing to regenerate
	}

	// Regenerate .mcp.json - WriteMCPJsonFromConfig checks pool status
	// and writes socket configs if pool is running
	if err := WriteMCPJsonFromConfig(i.ProjectPath, localMCPs); err != nil {
		log.Printf("[MCP-DEBUG] Failed to regenerate .mcp.json: %v", err)
		return fmt.Errorf("failed to regenerate .mcp.json: %w", err)
	}

	log.Printf("[MCP-DEBUG] Regenerated .mcp.json for %s with %d MCPs", i.Title, len(localMCPs))
	return nil
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
