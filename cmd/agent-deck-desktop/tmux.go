package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SessionInfo represents an Agent Deck session for the frontend.
type SessionInfo struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	ProjectPath string `json:"projectPath"`
	GroupPath   string `json:"groupPath"`
	Tool        string `json:"tool"`
	Status      string `json:"status"`
	TmuxSession string `json:"tmuxSession"`
	IsRemote    bool   `json:"isRemote"`
	RemoteHost  string `json:"remoteHost,omitempty"`
}

// sessionsJSON mirrors the storage format from internal/session/storage.go
type sessionsJSON struct {
	Instances []instanceJSON `json:"instances"`
}

type instanceJSON struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	ProjectPath string    `json:"project_path"`
	GroupPath   string    `json:"group_path"`
	Tool        string    `json:"tool"`
	Status      string    `json:"status"`
	TmuxSession string    `json:"tmux_session"`
	CreatedAt   time.Time `json:"created_at"`
	RemoteHost  string    `json:"remote_host,omitempty"`
}

// TmuxManager handles tmux session operations.
type TmuxManager struct{}

// NewTmuxManager creates a new TmuxManager.
func NewTmuxManager() *TmuxManager {
	return &TmuxManager{}
}

// ListSessions returns all Agent Deck sessions from sessions.json.
func (tm *TmuxManager) ListSessions() ([]SessionInfo, error) {
	// Find sessions.json path
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	sessionsPath := filepath.Join(home, ".agent-deck", "profiles", "default", "sessions.json")

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
		// Check if tmux session actually exists
		_, exists := runningTmux[inst.TmuxSession]

		// Skip sessions without tmux or remote sessions (for now)
		isRemote := inst.RemoteHost != ""
		if !exists && !isRemote {
			continue
		}

		result = append(result, SessionInfo{
			ID:          inst.ID,
			Title:       inst.Title,
			ProjectPath: inst.ProjectPath,
			GroupPath:   inst.GroupPath,
			Tool:        inst.Tool,
			Status:      inst.Status,
			TmuxSession: inst.TmuxSession,
			IsRemote:    isRemote,
			RemoteHost:  inst.RemoteHost,
		})
	}

	return result, nil
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
func (tm *TmuxManager) CreateSession(projectPath, title, tool string) (SessionInfo, error) {
	// Generate unique session name
	sessionName := fmt.Sprintf("agentdeck_%d", time.Now().UnixNano())

	// Create tmux session
	cmd := exec.Command("tmux", "new-session", "-d", "-s", sessionName, "-c", projectPath)
	if err := cmd.Run(); err != nil {
		return SessionInfo{}, fmt.Errorf("failed to create tmux session: %w", err)
	}

	// Build tool command
	var toolCmd string
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

	// Send the tool command to the session
	sendCmd := exec.Command("tmux", "send-keys", "-t", sessionName, toolCmd, "Enter")
	if err := sendCmd.Run(); err != nil {
		// Don't fail if we can't send the command, the session is still usable
		fmt.Printf("Warning: failed to send tool command: %v\n", err)
	}

	return SessionInfo{
		ID:          sessionName,
		Title:       title,
		ProjectPath: projectPath,
		Tool:        tool,
		Status:      "running",
		TmuxSession: sessionName,
	}, nil
}
