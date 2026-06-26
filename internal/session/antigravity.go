package session

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	antigravityConfigDirOverrideMu sync.RWMutex
	antigravityConfigDirOverride   string
)

// SetAntigravityAppDataDirOverrideForTest overrides ~/.gemini/antigravity-cli for tests.
func SetAntigravityAppDataDirOverrideForTest(dir string) {
	antigravityConfigDirOverrideMu.Lock()
	antigravityConfigDirOverride = dir
	antigravityConfigDirOverrideMu.Unlock()
}

// GetAntigravityAppDataDir returns ~/.gemini/antigravity-cli
func GetAntigravityAppDataDir() string {
	antigravityConfigDirOverrideMu.RLock()
	override := antigravityConfigDirOverride
	antigravityConfigDirOverrideMu.RUnlock()
	if override != "" {
		return override
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
	// Same rationale as parseAntigravityLatestUserPrompt: default 64 KiB token
	// limit would silently truncate large history.jsonl lines.
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
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
	if err := scanner.Err(); err != nil {
		sessionLog.Warn("antigravity_history_scan_error", slog.String("error", err.Error()))
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
	if len(antigravityModelCacheList) > 0 && time.Since(antigravityModelCacheTime) < antigravityModelCacheTTL {
		result := make([]string, len(antigravityModelCacheList))
		copy(result, antigravityModelCacheList)
		antigravityModelCacheMu.Unlock()
		return result, nil
	}
	antigravityModelCacheMu.Unlock()

	// Run `agy models` without holding the cache lock — a hung agy must not
	// block every concurrent caller. 10s is plenty for a local CLI subcommand.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, GetToolCommand("antigravity"), "models")
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
		fields := strings.Fields(line)
		if len(fields) > 0 {
			models = append(models, fields[0])
		}
	}
	if len(models) == 0 {
		return antigravityModelFallback, nil
	}
	sort.Strings(models)

	antigravityModelCacheMu.Lock()
	antigravityModelCacheList = models
	antigravityModelCacheTime = time.Now()
	antigravityModelCacheMu.Unlock()

	result := make([]string, len(models))
	copy(result, models)
	return result, nil
}

// ExtractAntigravityConversationIDFromPane scans tmux pane text for agy resume hint
func ExtractAntigravityConversationIDFromPane(text string) string {
	// Resume hints come in two shapes:
	//   agy --conversation=<uuid>
	//   agy -c <uuid>          (or -c=<uuid>)
	trimUUID := func(rest string) string {
		rest = strings.TrimSpace(rest)
		rest = strings.Trim(rest, "=)")
		if end := strings.IndexAny(rest, " \t()"); end > 0 {
			rest = rest[:end]
		}
		return rest
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if idx := strings.Index(line, "--conversation="); idx >= 0 {
			if id := trimUUID(line[idx+len("--conversation="):]); looksLikeUUID(id) {
				return id
			}
		}
		// Match `-c <uuid>` or `-c=<uuid>` but reject `-cfoo`. Anchor on
		// a leading space or start-of-line so we don't catch `--cdir` etc.
		for _, marker := range []string{" -c ", " -c="} {
			if idx := strings.Index(line, marker); idx >= 0 {
				if id := trimUUID(line[idx+len(marker):]); looksLikeUUID(id) {
					return id
				}
			}
		}
		if strings.HasPrefix(line, "-c ") {
			if id := trimUUID(line[3:]); looksLikeUUID(id) {
				return id
			}
		}
		if strings.HasPrefix(line, "-c=") {
			if id := trimUUID(line[3:]); looksLikeUUID(id) {
				return id
			}
		}
	}
	return ""
}
