package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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
	SessionID    string    // Full UUID
	Filename     string    // session-2025-12-26T15-09-4d8fcb4d.json
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

// findGeminiSessionInAllProjects searches all Gemini project directories for a session file
// This handles path hash mismatches when agent-deck runs from a different directory
// than where the Gemini session was originally created.
// Returns the full path to the session file, or empty string if not found.
func findGeminiSessionInAllProjects(sessionID string) string {
	if sessionID == "" || len(sessionID) < 8 {
		return ""
	}

	configDir := GetGeminiConfigDir()
	tmpDir := filepath.Join(configDir, "tmp")

	// List all project hash directories
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return ""
	}

	// Search pattern: session-*-<uuid8>.json
	targetPattern := "session-*-" + sessionID[:8] + ".json"

	var bestPath string
	var bestTime time.Time
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		chatsDir := filepath.Join(tmpDir, entry.Name(), "chats")
		pattern := filepath.Join(chatsDir, targetPattern)
		path, mtime := findNewestFile(pattern)
		if path != "" && mtime.After(bestTime) {
			bestPath = path
			bestTime = mtime
		}
	}

	return bestPath
}

// UpdateGeminiAnalyticsFromDisk updates the analytics struct from the session file on disk.
// Uses mtime caching to skip re-parsing unchanged files (important for 40MB+ session files).
func UpdateGeminiAnalyticsFromDisk(projectPath, sessionID string, analytics *GeminiSessionAnalytics) error {
	if sessionID == "" || len(sessionID) < 8 {
		return fmt.Errorf("invalid session ID")
	}

	sessionsDir := GetGeminiSessionsDir(projectPath)
	// Find file matching session ID prefix (first 8 chars)
	// Filename format: session-YYYY-MM-DDTHH-MM-<uuid8>.json
	pattern := filepath.Join(sessionsDir, "session-*-"+sessionID[:8]+".json")
	filePath, fileMtime := findNewestFile(pattern)

	// Fallback: search across all projects if not found in expected location
	if filePath == "" {
		if fallbackPath := findGeminiSessionInAllProjects(sessionID); fallbackPath != "" {
			filePath = fallbackPath
			if info, err := os.Stat(fallbackPath); err == nil {
				fileMtime = info.ModTime()
			}
		}
	}

	if filePath == "" {
		return fmt.Errorf("session file not found")
	}

	// mtime cache: skip re-parse if file hasn't changed since last read
	if !analytics.LastFileModTime.IsZero() && !fileMtime.IsZero() && fileMtime.Equal(analytics.LastFileModTime) {
		return nil
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read session file: %w", err)
	}

	var session struct {
		SessionID   string `json:"sessionId"`
		StartTime   string `json:"startTime"`
		LastUpdated string `json:"lastUpdated"`
		Messages    []struct {
			Type   string `json:"type"`
			Model  string `json:"model,omitempty"`
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
	analytics.Model = ""
	for _, msg := range session.Messages {
		if msg.Type == "gemini" {
			analytics.InputTokens += msg.Tokens.Input
			analytics.OutputTokens += msg.Tokens.Output
			analytics.TotalTurns++

			// For Gemini, the input tokens of the last message represent the total context size
			// including history and current prompt.
			analytics.CurrentContextTokens = msg.Tokens.Input

			// Extract model from the last gemini message that has one
			if msg.Model != "" {
				analytics.Model = msg.Model
			}
		}
	}

	// Record mtime for cache
	analytics.LastFileModTime = fileMtime

	return nil
}

// geminiModelCache holds cached model list from the Gemini API
var (
	geminiModelCacheMu    sync.Mutex
	geminiModelCacheList  []string
	geminiModelCacheTime  time.Time
	geminiModelCacheTTL   = 1 * time.Hour
)

// geminiModelFallback is the hardcoded fallback list when API is unavailable
var geminiModelFallback = []string{
	"gemini-2.5-flash",
	"gemini-2.5-flash-lite",
	"gemini-2.5-pro",
	"gemini-3-flash-preview",
	"gemini-3-pro-preview",
}

// GetAvailableGeminiModels returns a sorted list of Gemini models that support generateContent.
// Priority: 1) GEMINI_MODELS_OVERRIDE env var, 2) cached API result, 3) live API call, 4) fallback list.
func GetAvailableGeminiModels() ([]string, error) {
	// Priority 1: env var override (for testing)
	if override := os.Getenv("GEMINI_MODELS_OVERRIDE"); override != "" {
		models := strings.Split(override, ",")
		var result []string
		for _, m := range models {
			m = strings.TrimSpace(m)
			if m != "" {
				result = append(result, m)
			}
		}
		sort.Strings(result)
		return result, nil
	}

	// Priority 2: cache hit
	geminiModelCacheMu.Lock()
	defer geminiModelCacheMu.Unlock()

	if len(geminiModelCacheList) > 0 && time.Since(geminiModelCacheTime) < geminiModelCacheTTL {
		result := make([]string, len(geminiModelCacheList))
		copy(result, geminiModelCacheList)
		return result, nil
	}

	// Priority 3: API call (requires GOOGLE_API_KEY)
	apiKey := os.Getenv("GOOGLE_API_KEY")
	if apiKey == "" {
		// No API key, use fallback
		return geminiModelFallback, nil
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("https://generativelanguage.googleapis.com/v1beta/models?key=" + apiKey)
	if err != nil {
		return geminiModelFallback, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return geminiModelFallback, fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	var apiResp struct {
		Models []struct {
			Name                       string   `json:"name"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return geminiModelFallback, fmt.Errorf("failed to decode API response: %w", err)
	}

	// Filter to models that support generateContent
	var models []string
	for _, m := range apiResp.Models {
		supportsGenerate := false
		for _, method := range m.SupportedGenerationMethods {
			if method == "generateContent" {
				supportsGenerate = true
				break
			}
		}
		if !supportsGenerate {
			continue
		}
		// Strip "models/" prefix from name
		name := strings.TrimPrefix(m.Name, "models/")
		models = append(models, name)
	}

	sort.Strings(models)

	// Update cache
	geminiModelCacheList = models
	geminiModelCacheTime = time.Now()

	return models, nil
}

