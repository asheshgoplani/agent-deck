package costs

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// TranscriptEntry represents one line of a Claude transcript JSONL file.
// Handles both "assistant" entries (direct usage) and "progress" entries (subagent usage).
type TranscriptEntry struct {
	Type    string `json:"type"`
	UUID    string `json:"uuid"`
	Message struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	// Progress entries nest usage inside data.message.message
	Data      *progressData `json:"data,omitempty"`
	Timestamp string        `json:"timestamp"` // ISO 8601
}

type progressData struct {
	Message struct {
		Timestamp string `json:"timestamp"`
		Message   struct {
			Model string `json:"model"`
			Usage struct {
				InputTokens              int64 `json:"input_tokens"`
				OutputTokens             int64 `json:"output_tokens"`
				CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
				CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
	} `json:"message"`
}

// SyncResult holds the result of a historical sync operation.
type SyncResult struct {
	SessionsScanned int
	EventsImported  int
	EventsSkipped   int
	Errors          []string
}

// SyncSession holds the info needed to locate a session's transcript.
type SyncSession struct {
	InstanceID      string
	ClaudeSessionID string
	ProjectPath     string
	Tool            string
}

// SyncFromTranscripts reads historical usage from Claude transcript files
// and backfills cost_events for managed sessions.
func SyncFromTranscripts(store *Store, pricer *Pricer, sessions []SyncSession) SyncResult {
	var result SyncResult

	home, err := os.UserHomeDir()
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("get home dir: %v", err))
		return result
	}

	// Collect existing event IDs to avoid duplicates
	existing := make(map[string]bool)

	for _, sess := range sessions {
		if sess.Tool != "claude" || sess.ClaudeSessionID == "" {
			continue
		}

		result.SessionsScanned++

		// Derive transcript path: ~/.claude/projects/<slugified-path>/<session-id>.jsonl
		sluggedPath := slugifyProjectPath(sess.ProjectPath)
		transcriptPath := filepath.Join(home, ".claude", "projects", sluggedPath, sess.ClaudeSessionID+".jsonl")

		if _, err := os.Stat(transcriptPath); os.IsNotExist(err) {
			continue
		}

		events, errs := parseTranscriptFile(transcriptPath, sess.InstanceID, pricer)
		result.Errors = append(result.Errors, errs...)

		for _, ev := range events {
			// Check if we already have this event (by a deterministic ID)
			dedupKey := fmt.Sprintf("%s_%s", sess.InstanceID, ev.dedupKey)
			if existing[dedupKey] {
				result.EventsSkipped++
				continue
			}

			// Check if already in database
			var count int
			if err := store.db.QueryRow("SELECT COUNT(*) FROM cost_events WHERE id = ?", dedupKey).Scan(&count); err != nil {
				continue
			}
			if count > 0 {
				result.EventsSkipped++
				existing[dedupKey] = true
				continue
			}

			costEvent := CostEvent{
				ID:               dedupKey,
				SessionID:        sess.InstanceID,
				Timestamp:        ev.timestamp,
				Model:            ev.model,
				InputTokens:      ev.inputTokens,
				OutputTokens:     ev.outputTokens,
				CacheReadTokens:  ev.cacheReadTokens,
				CacheWriteTokens: ev.cacheWriteTokens,
				CostMicrodollars: pricer.ComputeCost(ev.model, ev.inputTokens, ev.outputTokens, ev.cacheReadTokens, ev.cacheWriteTokens),
			}

			if err := store.WriteCostEvent(costEvent); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("write event: %v", err))
				continue
			}
			existing[dedupKey] = true
			result.EventsImported++
		}
	}

	return result
}

type parsedUsage struct {
	dedupKey         string
	timestamp        time.Time
	model            string
	inputTokens      int64
	outputTokens     int64
	cacheReadTokens  int64
	cacheWriteTokens int64
}

func parseTranscriptFile(path, instanceID string, pricer *Pricer) ([]parsedUsage, []string) {
	f, err := os.Open(path)
	if err != nil {
		return nil, []string{fmt.Sprintf("open %s: %v", path, err)}
	}
	defer f.Close()

	var results []parsedUsage
	var errors []string

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024) // 10MB max line

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var entry TranscriptEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue // skip unparseable lines
		}

		var model string
		var inputTok, outputTok, cacheRead, cacheWrite int64
		var tsStr string

		switch entry.Type {
		case "assistant":
			usage := entry.Message.Usage
			model = entry.Message.Model
			inputTok = usage.InputTokens
			outputTok = usage.OutputTokens
			cacheRead = usage.CacheReadInputTokens
			cacheWrite = usage.CacheCreationInputTokens
			tsStr = entry.Timestamp

		case "progress":
			if entry.Data == nil {
				continue
			}
			usage := entry.Data.Message.Message.Usage
			model = entry.Data.Message.Message.Model
			inputTok = usage.InputTokens
			outputTok = usage.OutputTokens
			cacheRead = usage.CacheReadInputTokens
			cacheWrite = usage.CacheCreationInputTokens
			tsStr = entry.Data.Message.Timestamp
			if tsStr == "" {
				tsStr = entry.Timestamp
			}

		default:
			continue
		}

		if inputTok == 0 && outputTok == 0 {
			continue
		}

		ts, err := time.Parse(time.RFC3339Nano, tsStr)
		if err != nil {
			ts = time.Now()
		}

		dedupKey := entry.UUID
		if dedupKey == "" {
			dedupKey = uuid.NewString()
		}

		results = append(results, parsedUsage{
			dedupKey:         dedupKey,
			timestamp:        ts,
			model:            model,
			inputTokens:      inputTok,
			outputTokens:     outputTok,
			cacheReadTokens:  cacheRead,
			cacheWriteTokens: cacheWrite,
		})
	}

	if err := scanner.Err(); err != nil {
		errors = append(errors, fmt.Sprintf("scan %s: %v", path, err))
	}

	return results, errors
}

// slugifyProjectPath converts a project path to Claude's directory slug format.
// /home/user/Documents/Projects/foo -> -home-user-Documents-Projects-foo
// Claude replaces / with - and also . with -, and trims trailing slashes.
func slugifyProjectPath(projectPath string) string {
	projectPath = strings.TrimRight(projectPath, "/")
	slug := strings.ReplaceAll(projectPath, "/", "-")
	slug = strings.ReplaceAll(slug, ".", "-")
	return slug
}
