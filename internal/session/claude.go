package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ClaudeProject represents a project entry in Claude's config
type ClaudeProject struct {
	LastSessionId string `json:"lastSessionId"`
}

// ClaudeConfig represents the structure of .claude.json
type ClaudeConfig struct {
	Projects map[string]ClaudeProject `json:"projects"`
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
	// Convert project path to Claude's directory format
	// /Users/ashesh/claude-deck -> -Users-ashesh-claude-deck
	projectDirName := strings.ReplaceAll(projectPath, "/", "-")
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

		info, err := os.Stat(file)
		if err != nil {
			continue
		}

		// Find the most recently modified file
		if info.ModTime().After(mostRecentTime) {
			mostRecentTime = info.ModTime()
			mostRecent = strings.TrimSuffix(base, ".jsonl")
		}
	}

	// Only return if modified within last 5 minutes (actively used)
	if mostRecent != "" && time.Since(mostRecentTime) < 5*time.Minute {
		return mostRecent
	}

	return ""
}

// FindSessionForInstance finds the session file for a specific instance
// Parameters:
//   - projectPath: the project directory
//   - createdAfter: only consider files with internal timestamp >= this time
//   - excludeIDs: session IDs already claimed by other instances
// Returns the session ID or empty string if not found
func FindSessionForInstance(projectPath string, createdAfter time.Time, excludeIDs map[string]bool) string {
	configDir := GetClaudeConfigDir()

	// Convert project path to Claude's directory format
	// /Users/ashesh/claude-deck -> -Users-ashesh-claude-deck
	projectDirName := strings.ReplaceAll(projectPath, "/", "-")
	projectDir := filepath.Join(configDir, "projects", projectDirName)

	// Check if project directory exists
	if _, err := os.Stat(projectDir); os.IsNotExist(err) {
		return ""
	}

	// Find all UUID-named session files
	files, err := filepath.Glob(filepath.Join(projectDir, "*.jsonl"))
	if err != nil || len(files) == 0 {
		return ""
	}

	// UUID pattern for session files
	uuidPattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.jsonl$`)

	type candidate struct {
		sessionID string
		timestamp time.Time
	}
	var candidates []candidate

	for _, file := range files {
		base := filepath.Base(file)

		// Skip agent files
		if strings.HasPrefix(base, "agent-") {
			continue
		}

		// Only UUID-named files
		if !uuidPattern.MatchString(base) {
			continue
		}

		sessionID := strings.TrimSuffix(base, ".jsonl")

		// Skip if already claimed
		if excludeIDs[sessionID] {
			continue
		}

		// Get internal timestamp from file
		ts := getFileInternalTimestamp(file)
		if ts.IsZero() {
			continue
		}

		// Only consider files created after our session start
		if ts.Before(createdAfter) {
			continue
		}

		candidates = append(candidates, candidate{sessionID: sessionID, timestamp: ts})
	}

	if len(candidates) == 0 {
		return ""
	}

	// Sort by timestamp (earliest first) and return the first one
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].timestamp.Before(candidates[j].timestamp)
	})

	return candidates[0].sessionID
}

// getFileInternalTimestamp reads a session file and extracts the earliest timestamp
// It scans up to 10 lines because some files start with summary lines that have no timestamp
func getFileInternalTimestamp(filePath string) time.Time {
	file, err := os.Open(filePath)
	if err != nil {
		return time.Time{}
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)

	// Scan up to 10 lines looking for a timestamp
	for i := 0; i < 10 && scanner.Scan(); i++ {
		line := scanner.Text()

		// Parse JSON to get timestamp field (direct or in snapshot)
		var data struct {
			Timestamp string `json:"timestamp"`
			Snapshot  struct {
				Timestamp string `json:"timestamp"`
			} `json:"snapshot"`
		}
		if err := json.Unmarshal([]byte(line), &data); err != nil {
			continue
		}

		// Try direct timestamp first, then snapshot.timestamp
		tsStr := data.Timestamp
		if tsStr == "" {
			tsStr = data.Snapshot.Timestamp
		}
		if tsStr == "" {
			continue
		}

		// Parse ISO 8601 timestamp
		ts, err := time.Parse(time.RFC3339, tsStr)
		if err != nil {
			// Try parsing with milliseconds
			ts, err = time.Parse("2006-01-02T15:04:05.999Z", tsStr)
			if err != nil {
				continue
			}
		}

		return ts
	}

	return time.Time{}
}
