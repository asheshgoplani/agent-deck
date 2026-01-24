package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// SessionInfo represents an Agent Deck session for the frontend.
type SessionInfo struct {
	ID               string    `json:"id"`
	Title            string    `json:"title"`
	CustomLabel      string    `json:"customLabel,omitempty"`
	ProjectPath      string    `json:"projectPath"`
	GroupPath        string    `json:"groupPath"`
	Tool             string    `json:"tool"`
	Status           string    `json:"status"`
	TmuxSession      string    `json:"tmuxSession"`
	IsRemote         bool      `json:"isRemote"`
	RemoteHost       string    `json:"remoteHost,omitempty"`
	GitBranch        string    `json:"gitBranch,omitempty"`
	IsWorktree       bool      `json:"isWorktree,omitempty"`
	GitDirty         bool      `json:"gitDirty,omitempty"`
	GitAhead         int       `json:"gitAhead,omitempty"`
	GitBehind        int       `json:"gitBehind,omitempty"`
	LastAccessedAt   time.Time `json:"lastAccessedAt,omitempty"`
	LaunchConfigName string    `json:"launchConfigName,omitempty"`
	LoadedMCPs       []string  `json:"loadedMcps,omitempty"`
	DangerousMode    bool      `json:"dangerousMode,omitempty"`
}

// sessionsJSON mirrors the storage format from internal/session/storage.go
type sessionsJSON struct {
	Instances []instanceJSON `json:"instances"`
}

type instanceJSON struct {
	ID               string    `json:"id"`
	Title            string    `json:"title"`
	CustomLabel      string    `json:"custom_label,omitempty"`
	ProjectPath      string    `json:"project_path"`
	GroupPath        string    `json:"group_path"`
	Tool             string    `json:"tool"`
	Status           string    `json:"status"`
	TmuxSession      string    `json:"tmux_session"`
	CreatedAt        time.Time `json:"created_at"`
	LastAccessedAt   time.Time `json:"last_accessed_at,omitempty"`
	RemoteHost       string    `json:"remote_host,omitempty"`
	LaunchConfigName string    `json:"launch_config_name,omitempty"`
	LoadedMCPNames   []string  `json:"loaded_mcp_names,omitempty"`
	DangerousMode    bool      `json:"dangerous_mode,omitempty"`
}

// TmuxManager handles tmux session operations.
type TmuxManager struct{}

// NewTmuxManager creates a new TmuxManager.
func NewTmuxManager() *TmuxManager {
	return &TmuxManager{}
}

// getSessionsPath returns the path to sessions.json for the default profile.
func (tm *TmuxManager) getSessionsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".agent-deck", "profiles", "default", "sessions.json"), nil
}

// generateSessionID creates a unique session ID matching the TUI format: {8-char-hex}-{unix-timestamp}
func generateSessionID() string {
	bytes := make([]byte, 4) // 4 bytes = 8 hex chars
	if _, err := rand.Read(bytes); err != nil {
		// Fallback to timestamp if random fails
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%s-%d", hex.EncodeToString(bytes), time.Now().Unix())
}

// countSessionsAtPath returns the number of existing sessions at a given project path.
func (tm *TmuxManager) countSessionsAtPath(projectPath string) int {
	sessionsPath, err := tm.getSessionsPath()
	if err != nil {
		return 0
	}

	data, err := os.ReadFile(sessionsPath)
	if err != nil {
		return 0
	}

	var sessions sessionsJSON
	if err := json.Unmarshal(data, &sessions); err != nil {
		return 0
	}

	// Normalize the path for comparison
	normalizedPath := filepath.Clean(projectPath)

	count := 0
	for _, inst := range sessions.Instances {
		if filepath.Clean(inst.ProjectPath) == normalizedPath {
			count++
		}
	}
	return count
}

// PersistSession adds a new session to sessions.json.
func (tm *TmuxManager) PersistSession(s SessionInfo) error {
	sessionsPath, err := tm.getSessionsPath()
	if err != nil {
		return err
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(sessionsPath), 0700); err != nil {
		return fmt.Errorf("failed to create sessions directory: %w", err)
	}

	// Read current sessions (or create empty structure if file doesn't exist)
	var raw map[string]json.RawMessage
	data, err := os.ReadFile(sessionsPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Initialize empty structure
			raw = map[string]json.RawMessage{
				"instances":  json.RawMessage("[]"),
				"updated_at": json.RawMessage(`"` + time.Now().Format(time.RFC3339Nano) + `"`),
			}
		} else {
			return fmt.Errorf("failed to read sessions file: %w", err)
		}
	} else {
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("failed to parse sessions file: %w", err)
		}
	}

	// Parse instances array using typed struct
	var instances []instanceJSON
	if rawInstances, ok := raw["instances"]; ok {
		if err := json.Unmarshal(rawInstances, &instances); err != nil {
			return fmt.Errorf("failed to parse instances: %w", err)
		}
	}

	// Create new instance entry using typed struct (provides compile-time safety)
	now := time.Now()
	newInstance := instanceJSON{
		ID:               s.ID,
		Title:            s.Title,
		CustomLabel:      s.CustomLabel,
		ProjectPath:      s.ProjectPath,
		GroupPath:        s.GroupPath,
		Tool:             s.Tool,
		Status:           s.Status,
		TmuxSession:      s.TmuxSession,
		CreatedAt:        now,
		LastAccessedAt:   now,
		LaunchConfigName: s.LaunchConfigName,
		LoadedMCPNames:   s.LoadedMCPs,
		DangerousMode:    s.DangerousMode,
	}

	// Append new instance
	instances = append(instances, newInstance)

	// Marshal instances back
	instancesData, err := json.Marshal(instances)
	if err != nil {
		return fmt.Errorf("failed to marshal instances: %w", err)
	}
	raw["instances"] = instancesData

	// Update timestamp
	updatedAt, _ := json.Marshal(now.Format(time.RFC3339Nano))
	raw["updated_at"] = updatedAt

	// Write back with indentation (atomic write via temp file)
	output, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	tmpPath := sessionsPath + ".tmp"
	if err := os.WriteFile(tmpPath, output, 0600); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	if err := os.Rename(tmpPath, sessionsPath); err != nil {
		os.Remove(tmpPath) // Clean up temp file on failure
		return fmt.Errorf("failed to finalize save: %w", err)
	}

	return nil
}

// ListSessions returns all Agent Deck sessions from sessions.json.
func (tm *TmuxManager) ListSessions() ([]SessionInfo, error) {
	sessionsPath, err := tm.getSessionsPath()
	if err != nil {
		return nil, err
	}

	// Read and parse sessions.json
	data, err := os.ReadFile(sessionsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []SessionInfo{}, nil
		}
		return nil, err
	}

	var sessions sessionsJSON
	if err := json.Unmarshal(data, &sessions); err != nil {
		return nil, err
	}

	// Get list of running tmux sessions
	runningTmux := tm.getRunningTmuxSessions()

	// Convert to SessionInfo
	result := make([]SessionInfo, 0, len(sessions.Instances))
	for _, inst := range sessions.Instances {
		// Check if tmux session actually exists (for local sessions)
		_, exists := runningTmux[inst.TmuxSession]
		isRemote := inst.RemoteHost != ""

		// Skip local sessions without a running tmux session
		// Remote sessions are included even without local tmux verification
		if !exists && !isRemote {
			continue
		}

		// Get git info for the project path (only for local sessions)
		var gitInfo GitInfo
		if !isRemote {
			gitInfo = tm.getGitInfo(inst.ProjectPath)
		}

		// Use LastAccessedAt if set, otherwise fall back to CreatedAt
		lastAccessed := inst.LastAccessedAt
		if lastAccessed.IsZero() {
			lastAccessed = inst.CreatedAt
		}

		result = append(result, SessionInfo{
			ID:               inst.ID,
			Title:            inst.Title,
			CustomLabel:      inst.CustomLabel,
			ProjectPath:      inst.ProjectPath,
			GroupPath:        inst.GroupPath,
			Tool:             inst.Tool,
			Status:           inst.Status,
			TmuxSession:      inst.TmuxSession,
			IsRemote:         isRemote,
			RemoteHost:       inst.RemoteHost,
			GitBranch:        gitInfo.Branch,
			IsWorktree:       gitInfo.IsWorktree,
			GitDirty:         gitInfo.IsDirty,
			GitAhead:         gitInfo.Ahead,
			GitBehind:        gitInfo.Behind,
			LastAccessedAt:   lastAccessed,
			LaunchConfigName: inst.LaunchConfigName,
			LoadedMCPs:       inst.LoadedMCPNames,
			DangerousMode:    inst.DangerousMode,
		})
	}

	// Sort by LastAccessedAt (most recent first)
	sort.Slice(result, func(i, j int) bool {
		return result[i].LastAccessedAt.After(result[j].LastAccessedAt)
	})

	return result, nil
}

// GitInfo contains git repository information for a session.
type GitInfo struct {
	Branch     string
	IsWorktree bool
	IsDirty    bool
	Ahead      int
	Behind     int
}

// getGitInfo returns comprehensive git information for a project path.
func (tm *TmuxManager) getGitInfo(projectPath string) GitInfo {
	info := GitInfo{}
	if projectPath == "" {
		return info
	}

	// Get git branch
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = projectPath
	output, err := cmd.Output()
	if err != nil {
		return info
	}
	info.Branch = strings.TrimSpace(string(output))

	// Check if worktree: .git is a file (worktree) vs directory (main clone)
	gitPath := filepath.Join(projectPath, ".git")
	fileInfo, err := os.Stat(gitPath)
	if err == nil {
		info.IsWorktree = !fileInfo.IsDir()
	}

	// Check if dirty: git status --porcelain returns non-empty if there are changes
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = projectPath
	statusOutput, err := statusCmd.Output()
	if err == nil {
		info.IsDirty = len(strings.TrimSpace(string(statusOutput))) > 0
	}

	// Get ahead/behind counts: git rev-list --left-right --count HEAD...@{u}
	// Returns "ahead\tbehind" or errors if no upstream
	aheadBehindCmd := exec.Command("git", "rev-list", "--left-right", "--count", "HEAD...@{u}")
	aheadBehindCmd.Dir = projectPath
	aheadBehindOutput, err := aheadBehindCmd.Output()
	if err == nil {
		parts := strings.Fields(strings.TrimSpace(string(aheadBehindOutput)))
		if len(parts) == 2 {
			fmt.Sscanf(parts[0], "%d", &info.Ahead)
			fmt.Sscanf(parts[1], "%d", &info.Behind)
		}
	}

	return info
}

// getRunningTmuxSessions returns a map of currently running tmux session names.
func (tm *TmuxManager) getRunningTmuxSessions() map[string]bool {
	result := make(map[string]bool)

	// Run: tmux list-sessions -F "#{session_name}"
	cmd := exec.Command("tmux", "list-sessions", "-F", "#{session_name}")
	output, err := cmd.Output()
	if err != nil {
		// tmux might not be running or no sessions exist
		return result
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line != "" {
			result[line] = true
		}
	}

	return result
}

// GetScrollback captures the scrollback buffer from a tmux session.
func (tm *TmuxManager) GetScrollback(tmuxSession string, lines int) (string, error) {
	if lines <= 0 {
		lines = 10000
	}

	// tmux capture-pane -t <session> -p -S -<lines>
	// -e preserves escape sequences (colors)
	cmd := exec.Command("tmux", "capture-pane", "-t", tmuxSession, "-p", "-e", "-S", fmt.Sprintf("-%d", lines))
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	// Convert LF to CRLF for xterm.js
	// tmux outputs \n line endings, but xterm.js interprets \n as "move down"
	// without returning to column 0. We need \r\n for proper rendering.
	content := string(output)
	content = strings.ReplaceAll(content, "\r\n", "\n") // Normalize any existing CRLF
	content = strings.ReplaceAll(content, "\n", "\r\n") // Convert all LF to CRLF

	return content, nil
}

// SessionExists checks if a tmux session exists.
func (tm *TmuxManager) SessionExists(tmuxSession string) bool {
	cmd := exec.Command("tmux", "has-session", "-t", tmuxSession)
	return cmd.Run() == nil
}

// CreateSession creates a new tmux session and launches an AI tool.
// If configKey is non-empty, the launch config settings will be applied.
// The session is persisted to sessions.json so it survives app restarts.
func (tm *TmuxManager) CreateSession(projectPath, title, tool, configKey string) (SessionInfo, error) {
	// Generate unique session ID (matches TUI format: {8-char-hex}-{unix-timestamp})
	sessionID := generateSessionID()

	// Generate unique tmux session name
	sessionName := fmt.Sprintf("agentdeck_%d", time.Now().UnixNano())

	// Create tmux session
	cmd := exec.Command("tmux", "new-session", "-d", "-s", sessionName, "-c", projectPath)
	if err := cmd.Run(); err != nil {
		return SessionInfo{}, fmt.Errorf("failed to create tmux session: %w", err)
	}

	// Build tool command with optional launch config settings
	var toolCmd string
	var cmdArgs []string
	var dangerousMode bool
	var loadedMCPs []string
	var launchConfigName string

	// Get base tool command
	switch tool {
	case "claude":
		toolCmd = "claude"
	case "gemini":
		toolCmd = "gemini"
	case "opencode":
		toolCmd = "opencode"
	default:
		toolCmd = tool // Allow custom tools
	}

	// Apply launch config if provided
	if configKey != "" {
		cfg := session.GetLaunchConfigByKey(configKey)
		if cfg != nil {
			launchConfigName = cfg.Name
			dangerousMode = cfg.DangerousMode

			// Add dangerous mode flag
			if cfg.DangerousMode {
				switch tool {
				case "claude":
					cmdArgs = append(cmdArgs, "--dangerously-skip-permissions")
				case "gemini":
					cmdArgs = append(cmdArgs, "--yolo")
				}
			}

			// Add MCP config path if specified
			if cfg.MCPConfigPath != "" {
				expandedPath, err := cfg.ExpandMCPConfigPath()
				if err == nil && expandedPath != "" {
					switch tool {
					case "claude":
						cmdArgs = append(cmdArgs, "--mcp-config", expandedPath)
					}
					// Parse MCP names for display
					if mcpNames, err := cfg.ParseMCPNames(); err == nil {
						loadedMCPs = mcpNames
					}
				}
			}

			// Add extra args
			cmdArgs = append(cmdArgs, cfg.ExtraArgs...)
		}
	}

	// Build the full command string
	fullCmd := toolCmd
	if len(cmdArgs) > 0 {
		fullCmd = toolCmd + " " + strings.Join(cmdArgs, " ")
	}

	// Send the tool command to the session
	sendCmd := exec.Command("tmux", "send-keys", "-t", sessionName, fullCmd, "Enter")
	if err := sendCmd.Run(); err != nil {
		// Don't fail if we can't send the command, the session is still usable
		fmt.Printf("Warning: failed to send tool command: %v\n", err)
	}

	// Count existing sessions at this path to auto-generate label for duplicates
	count := tm.countSessionsAtPath(projectPath)
	customLabel := ""
	if count > 0 {
		customLabel = fmt.Sprintf("#%d", count+1)
	}

	// Build session info
	sessionInfo := SessionInfo{
		ID:               sessionID, // Use proper ID, not tmux name
		Title:            title,
		CustomLabel:      customLabel,
		ProjectPath:      projectPath,
		Tool:             tool,
		Status:           "running",
		TmuxSession:      sessionName,
		LaunchConfigName: launchConfigName,
		LoadedMCPs:       loadedMCPs,
		DangerousMode:    dangerousMode,
		LastAccessedAt:   time.Now(),
	}

	// Persist to sessions.json so session survives app restarts
	if err := tm.PersistSession(sessionInfo); err != nil {
		// Log warning but don't fail - session is still usable
		fmt.Printf("Warning: failed to persist session: %v\n", err)
	}

	return sessionInfo, nil
}

// MarkSessionAccessed updates the last_accessed_at timestamp for a session.
// This keeps the session list sorted by most recently used.
func (tm *TmuxManager) MarkSessionAccessed(sessionID string) error {
	sessionsPath, err := tm.getSessionsPath()
	if err != nil {
		return err
	}

	// Read current sessions
	data, err := os.ReadFile(sessionsPath)
	if err != nil {
		return err
	}

	// Parse as raw JSON to preserve all fields
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// Parse instances array
	var instances []map[string]interface{}
	if err := json.Unmarshal(raw["instances"], &instances); err != nil {
		return err
	}

	// Find and update the session
	found := false
	for i, inst := range instances {
		if id, ok := inst["id"].(string); ok && id == sessionID {
			instances[i]["last_accessed_at"] = time.Now().Format(time.RFC3339Nano)
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	// Marshal instances back
	instancesData, err := json.Marshal(instances)
	if err != nil {
		return err
	}
	raw["instances"] = instancesData

	// Write back with indentation (atomic write via temp file)
	output, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}

	tmpPath := sessionsPath + ".tmp"
	if err := os.WriteFile(tmpPath, output, 0600); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	if err := os.Rename(tmpPath, sessionsPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to finalize save: %w", err)
	}

	return nil
}

// UpdateSessionCustomLabel updates the custom_label field for a session.
// Pass an empty string to remove the custom label.
func (tm *TmuxManager) UpdateSessionCustomLabel(sessionID, customLabel string) error {
	sessionsPath, err := tm.getSessionsPath()
	if err != nil {
		return err
	}

	// Read current sessions
	data, err := os.ReadFile(sessionsPath)
	if err != nil {
		return err
	}

	// Parse as raw JSON to preserve all fields
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// Parse instances array
	var instances []map[string]interface{}
	if err := json.Unmarshal(raw["instances"], &instances); err != nil {
		return err
	}

	// Find and update the session
	found := false
	for i, inst := range instances {
		if id, ok := inst["id"].(string); ok && id == sessionID {
			if customLabel == "" {
				// Remove the custom_label field
				delete(instances[i], "custom_label")
			} else {
				instances[i]["custom_label"] = customLabel
			}
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	// Marshal instances back
	instancesData, err := json.Marshal(instances)
	if err != nil {
		return err
	}
	raw["instances"] = instancesData

	// Write back with indentation (atomic write via temp file)
	output, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}

	tmpPath := sessionsPath + ".tmp"
	if err := os.WriteFile(tmpPath, output, 0600); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	if err := os.Rename(tmpPath, sessionsPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to finalize save: %w", err)
	}

	return nil
}
