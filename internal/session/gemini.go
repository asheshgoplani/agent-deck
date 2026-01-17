package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// geminiConfigDirOverride allows tests to override config directory
var geminiConfigDirOverride string

// GetGeminiConfigDir returns ~/.gemini
// Unlike Claude, Gemini has no GEMINI_CONFIG_DIR env var override
func GetGeminiConfigDir() string {
	if geminiConfigDirOverride != "" {
		return geminiConfigDirOverride
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini")
}

// HashProjectPath generates SHA256 hash of absolute project path
// This matches Gemini CLI's project hash algorithm for session storage
// VERIFIED: echo -n "/Users/ashesh" | shasum -a 256
// NOTE: Must resolve symlinks (e.g., /tmp -> /private/tmp on macOS)
func HashProjectPath(projectPath string) string {
	absPath, err := filepath.Abs(projectPath)
	if err != nil {
		return ""
	}
	// Resolve symlinks to match Gemini CLI behavior
	// macOS: /tmp is symlink to /private/tmp
	realPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		// Fall back to absPath if symlink resolution fails
		realPath = absPath
	}
	hash := sha256.Sum256([]byte(realPath))
	return hex.EncodeToString(hash[:])
}

// GetGeminiSessionsDir returns the chats directory for a project
// Format: ~/.gemini/tmp/<project_hash>/chats/
func GetGeminiSessionsDir(projectPath string) string {
	configDir := GetGeminiConfigDir()
	projectHash := HashProjectPath(projectPath)
	if projectHash == "" {
		return "" // Cannot determine sessions dir without valid hash
	}
	return filepath.Join(configDir, "tmp", projectHash, "chats")
}

// GeminiSessionInfo holds parsed session metadata
type GeminiSessionInfo struct {
	SessionID    string // Full UUID
	Filename     string // session-2025-12-26T15-09-4d8fcb4d.json
	StartTime    time.Time
	LastUpdated  time.Time
	MessageCount int
}

// parseGeminiSessionFile reads a session file and extracts metadata
// VERIFIED: Field names use camelCase (sessionId, not session_id)
func parseGeminiSessionFile(filePath string) (GeminiSessionInfo, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return GeminiSessionInfo{}, fmt.Errorf("failed to read session file: %w", err)
	}

	var session struct {
		SessionID   string            `json:"sessionId"` // VERIFIED: camelCase
		StartTime   string            `json:"startTime"`
		LastUpdated string            `json:"lastUpdated"`
		Messages    []json.RawMessage `json:"messages"`
	}

	if err := json.Unmarshal(data, &session); err != nil {
		return GeminiSessionInfo{}, fmt.Errorf("failed to parse session: %w", err)
	}

	// Parse timestamps with fallback for milliseconds (like claude.go)
	startTime, err := time.Parse(time.RFC3339, session.StartTime)
	if err != nil {
		// Try with milliseconds (Gemini uses .999Z format)
		startTime, _ = time.Parse("2006-01-02T15:04:05.999Z", session.StartTime)
	}

	lastUpdated, err := time.Parse(time.RFC3339, session.LastUpdated)
	if err != nil {
		// Try with milliseconds
		lastUpdated, _ = time.Parse("2006-01-02T15:04:05.999Z", session.LastUpdated)
	}

	return GeminiSessionInfo{
		SessionID:    session.SessionID,
		Filename:     filepath.Base(filePath),
		StartTime:    startTime,
		LastUpdated:  lastUpdated,
		MessageCount: len(session.Messages),
	}, nil
}

// ListGeminiSessions returns all sessions for a project path
// Scans ~/.gemini/tmp/<hash>/chats/ and parses session files
// Sorted by LastUpdated (most recent first)
func ListGeminiSessions(projectPath string) ([]GeminiSessionInfo, error) {
	sessionsDir := GetGeminiSessionsDir(projectPath)
	files, err := filepath.Glob(filepath.Join(sessionsDir, "session-*.json"))
	if err != nil {
		return nil, err
	}

	var sessions []GeminiSessionInfo
	for _, file := range files {
		info, err := parseGeminiSessionFile(file)
		if err != nil {
			continue // Skip malformed files
		}
		sessions = append(sessions, info)
	}

	// Sort by LastUpdated (most recent first)
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].LastUpdated.After(sessions[j].LastUpdated)
	})

	return sessions, nil
}

// UpdateGeminiAnalyticsFromDisk updates the analytics struct from the session file on disk
func UpdateGeminiAnalyticsFromDisk(projectPath, sessionID string, analytics *GeminiSessionAnalytics) error {
	if sessionID == "" || len(sessionID) < 8 {
		return fmt.Errorf("invalid session ID")
	}

	sessionsDir := GetGeminiSessionsDir(projectPath)
	// Find file matching session ID prefix (first 8 chars)
	// Filename format: session-YYYY-MM-DDTHH-MM-<uuid8>.json
	pattern := filepath.Join(sessionsDir, "session-*-"+sessionID[:8]+".json")
	files, _ := filepath.Glob(pattern)
	if len(files) == 0 {
		return fmt.Errorf("session file not found")
	}

	data, err := os.ReadFile(files[0])
	if err != nil {
		return fmt.Errorf("failed to read session file: %w", err)
	}

	var session struct {
		SessionID   string `json:"sessionId"`
		StartTime   string `json:"startTime"`
		LastUpdated string `json:"lastUpdated"`
		Messages    []struct {
			Type   string `json:"type"`
			Tokens struct {
				Input  int `json:"input"`
				Output int `json:"output"`
			} `json:"tokens"`
		} `json:"messages"`
	}

	if err := json.Unmarshal(data, &session); err != nil {
		return fmt.Errorf("failed to parse session for analytics: %w", err)
	}

	// Parse timestamps
	startTime, _ := time.Parse(time.RFC3339, session.StartTime)
	if startTime.IsZero() {
		startTime, _ = time.Parse("2006-01-02T15:04:05.999Z", session.StartTime)
	}
	lastUpdated, _ := time.Parse(time.RFC3339, session.LastUpdated)
	if lastUpdated.IsZero() {
		lastUpdated, _ = time.Parse("2006-01-02T15:04:05.999Z", session.LastUpdated)
	}

	analytics.StartTime = startTime
	analytics.LastActive = lastUpdated
	if !startTime.IsZero() && !lastUpdated.IsZero() {
		analytics.Duration = lastUpdated.Sub(startTime)
	}

	// Reset and accumulate tokens
	analytics.InputTokens = 0
	analytics.OutputTokens = 0
	analytics.TotalTurns = 0
	for _, msg := range session.Messages {
		if msg.Type == "gemini" {
			analytics.InputTokens += msg.Tokens.Input
			analytics.OutputTokens += msg.Tokens.Output
			analytics.TotalTurns++

			// For Gemini, the input tokens of the last message represent the total context size
			// including history and current prompt.
			analytics.CurrentContextTokens = msg.Tokens.Input
		}
	}

	return nil
}
