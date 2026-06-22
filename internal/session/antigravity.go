package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var antigravityConfigDirOverride string

// SetAntigravityAppDataDirOverrideForTest overrides ~/.gemini/antigravity-cli for tests.
func SetAntigravityAppDataDirOverrideForTest(dir string) {
	antigravityConfigDirOverride = dir
}

// GetAntigravityAppDataDir returns ~/.gemini/antigravity-cli
func GetAntigravityAppDataDir() string {
	if antigravityConfigDirOverride != "" {
		return antigravityConfigDirOverride
	}
	return filepath.Join(GetGeminiConfigDir(), "antigravity-cli")
}

// GetAntigravityBrainDir returns ~/.gemini/antigravity-cli/brain
func GetAntigravityBrainDir() string {
	return filepath.Join(GetAntigravityAppDataDir(), "brain")
}

// GetAntigravityConfigDir returns ~/.gemini/config (shared Antigravity config root)
func GetAntigravityConfigDir() string {
	return filepath.Join(GetGeminiConfigDir(), "config")
}

// AntigravityConversationInfo holds parsed conversation metadata
type AntigravityConversationInfo struct {
	ConversationID string
	LastUpdated    time.Time
}

// antigravityConversationHasData reports whether a conversation ID has on-disk data
func antigravityConversationHasData(conversationID string) bool {
	if conversationID == "" {
		return false
	}
	brainDir := filepath.Join(GetAntigravityBrainDir(), conversationID)
	info, err := os.Stat(brainDir)
	return err == nil && info.IsDir()
}

// ListAntigravityConversations scans brain/ for conversation directories
func ListAntigravityConversations() ([]AntigravityConversationInfo, error) {
	brainDir := GetAntigravityBrainDir()
	entries, err := os.ReadDir(brainDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var conversations []AntigravityConversationInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		if !looksLikeUUID(id) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		conversations = append(conversations, AntigravityConversationInfo{
			ConversationID: id,
			LastUpdated:    info.ModTime(),
		})
	}

	sort.Slice(conversations, func(i, j int) bool {
		return conversations[i].LastUpdated.After(conversations[j].LastUpdated)
	})
	return conversations, nil
}

// findMostRecentAntigravityConversation returns the most recently modified conversation ID
func findMostRecentAntigravityConversation() string {
	conversations, err := ListAntigravityConversations()
	if err != nil || len(conversations) == 0 {
		return ""
	}
	return conversations[0].ConversationID
}

// parseAntigravityHistoryIndex reads history.jsonl for conversation IDs (fallback discovery)
func parseAntigravityHistoryIndex() []string {
	historyPath := filepath.Join(GetAntigravityAppDataDir(), "history.jsonl")
	f, err := os.Open(historyPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	var ids []string
	seen := make(map[string]struct{})
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record struct {
			ConversationID string `json:"conversationId"`
			ID             string `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		id := strings.TrimSpace(record.ConversationID)
		if id == "" {
			id = strings.TrimSpace(record.ID)
		}
		if id == "" || !looksLikeUUID(id) {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func looksLikeUUID(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
				return false
			}
		}
	}
	return true
}

var (
	antigravityModelCacheMu   sync.Mutex
	antigravityModelCacheList []string
	antigravityModelCacheTime time.Time
	antigravityModelCacheTTL  = 1 * time.Hour
)

var antigravityModelFallback = []string{
	"gemini-2.5-flash",
	"gemini-2.5-pro",
	"gemini-3-flash-preview",
	"gemini-3.1-pro-preview",
}

// GetAvailableAntigravityModels returns models from `agy models` with caching and fallback
func GetAvailableAntigravityModels() ([]string, error) {
	if override := os.Getenv("ANTIGRAVITY_MODELS_OVERRIDE"); override != "" {
		var result []string
		for _, m := range strings.Split(override, ",") {
			m = strings.TrimSpace(m)
			if m != "" {
				result = append(result, m)
			}
		}
		sort.Strings(result)
		return result, nil
	}

	antigravityModelCacheMu.Lock()
	defer antigravityModelCacheMu.Unlock()
	if len(antigravityModelCacheList) > 0 && time.Since(antigravityModelCacheTime) < antigravityModelCacheTTL {
		result := make([]string, len(antigravityModelCacheList))
		copy(result, antigravityModelCacheList)
		return result, nil
	}

	cmd := exec.Command(GetToolCommand("antigravity"), "models")
	out, err := cmd.Output()
	if err != nil {
		return antigravityModelFallback, fmt.Errorf("agy models: %w", err)
	}

	var models []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Usage") {
			continue
		}
		// Take first column / token
		fields := strings.Fields(line)
		if len(fields) > 0 {
			models = append(models, fields[0])
		}
	}
	if len(models) == 0 {
		return antigravityModelFallback, nil
	}
	sort.Strings(models)
	antigravityModelCacheList = models
	antigravityModelCacheTime = time.Now()
	result := make([]string, len(models))
	copy(result, models)
	return result, nil
}

// ExtractAntigravityConversationIDFromPane scans tmux pane text for agy resume hint
func ExtractAntigravityConversationIDFromPane(text string) string {
	// Resume: agy --conversation=d1d8a55b-cc27-4dd4-bc62-2f73015960d2 (or -c)
	const prefix = "--conversation="
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if idx := strings.Index(line, prefix); idx >= 0 {
			rest := line[idx+len(prefix):]
			rest = strings.TrimSpace(rest)
			rest = strings.Trim(rest, "=)")
			if end := strings.IndexAny(rest, " \t("); end > 0 {
				rest = rest[:end]
			}
			if looksLikeUUID(rest) {
				return rest
			}
		}
	}
	return ""
}
