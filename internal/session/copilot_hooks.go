package session

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// getCopilotHomeDir returns the Copilot CLI config/state directory.
// Respects COPILOT_CONFIG_DIR env var, falling back to ~/.copilot.
func getCopilotHomeDir() string {
	if dir := strings.TrimSpace(os.Getenv("COPILOT_CONFIG_DIR")); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".copilot")
	}
	return filepath.Join(home, ".copilot")
}

// getCopilotSessionStateDir returns the session state directory.
func getCopilotSessionStateDir() string {
	return filepath.Join(getCopilotHomeDir(), "session-state")
}

// copilotSessionStartEvent represents the first event in a Copilot events.jsonl.
type copilotSessionStartEvent struct {
	Type string `json:"type"`
	Data struct {
		SessionID string `json:"sessionId"`
		Context   struct {
			CWD string `json:"cwd"`
		} `json:"context"`
		StartTime string `json:"startTime"` // ISO 8601
	} `json:"data"`
	Timestamp string `json:"timestamp"` // ISO 8601
}

// detectCopilotSessionFromDisk scans ~/.copilot/session-state/ for the most
// recent session started from the given working directory after startedAfter.
// Returns the session ID or empty string if no match.
func detectCopilotSessionFromDisk(cwd string, startedAfter time.Time) string {
	sessionsDir := getCopilotSessionStateDir()
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return ""
	}

	type candidate struct {
		sessionID string
		startTime time.Time
	}
	var candidates []candidate

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		sessionID := entry.Name()
		eventsPath := filepath.Join(sessionsDir, sessionID, "events.jsonl")

		// Quick stat check — skip old sessions without reading
		info, err := os.Stat(eventsPath)
		if err != nil || info.ModTime().Before(startedAfter) {
			continue
		}

		evt := readCopilotSessionStart(eventsPath)
		if evt == nil || evt.Data.SessionID == "" {
			continue
		}

		// Parse start time
		ts, err := time.Parse(time.RFC3339Nano, evt.Data.StartTime)
		if err != nil {
			ts, err = time.Parse(time.RFC3339, evt.Data.StartTime)
			if err != nil {
				continue
			}
		}

		if ts.Before(startedAfter) {
			continue
		}

		// Match by working directory (normalize trailing slashes)
		eventCWD := strings.TrimRight(evt.Data.Context.CWD, "/")
		targetCWD := strings.TrimRight(cwd, "/")
		if eventCWD != targetCWD {
			continue
		}

		candidates = append(candidates, candidate{
			sessionID: evt.Data.SessionID,
			startTime: ts,
		})
	}

	if len(candidates) == 0 {
		return ""
	}

	// Return the most recent session
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].startTime.After(candidates[j].startTime)
	})
	return candidates[0].sessionID
}

// readCopilotSessionStart reads the first line of events.jsonl and parses
// the session.start event. Returns nil if the file is missing, empty, or
// the first event is not session.start.
func readCopilotSessionStart(eventsPath string) *copilotSessionStartEvent {
	f, err := os.Open(eventsPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	// Read only the first line (session.start is always first)
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1)
	for {
		n, err := f.Read(tmp)
		if n > 0 {
			if tmp[0] == '\n' {
				break
			}
			buf = append(buf, tmp[0])
		}
		if err != nil {
			break
		}
		// Safety limit: first line should not exceed 32KB
		if len(buf) > 32*1024 {
			return nil
		}
	}

	if len(buf) == 0 {
		return nil
	}

	var evt copilotSessionStartEvent
	if err := json.Unmarshal(buf, &evt); err != nil {
		return nil
	}

	if evt.Type != "session.start" {
		return nil
	}

	return &evt
}

// copilotSessionHasConversationData checks whether a Copilot session has
// meaningful conversation (user turns) in its events.jsonl. Used to decide
// whether --resume is appropriate vs starting fresh.
func copilotSessionHasConversationData(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	eventsPath := filepath.Join(getCopilotSessionStateDir(), sessionID, "events.jsonl")
	info, err := os.Stat(eventsPath)
	if err != nil {
		return false
	}
	// A fresh session has ~2-4KB for session.start + system.message.
	// Any user interaction adds user.message + assistant.* events.
	// Use 8KB as heuristic — anything larger likely has conversation data.
	return info.Size() > 8*1024
}

// buildCopilotCommand builds the copilot CLI command for an Instance.
// Handles new sessions, resume, model selection, and auto-approve mode.
func buildCopilotCommand(i *Instance) string {
	envPrefix := i.buildEnvSourceCommand()

	// Determine model flag
	modelFlag := ""
	if i.CopilotModel != "" {
		modelFlag = " --model " + i.CopilotModel
	} else if i.CopilotSessionID == "" {
		// Only apply default model for NEW sessions (not resumes)
		userConfig, _ := LoadUserConfig()
		if userConfig != nil && userConfig.Copilot.DefaultModel != "" {
			modelFlag = " --model " + userConfig.Copilot.DefaultModel
		}
	}

	// Determine allow-all flag
	allowAllFlag := ""
	if i.CopilotAllowAll {
		allowAllFlag = " --allow-all"
	} else {
		userConfig, _ := LoadUserConfig()
		if userConfig != nil && userConfig.Copilot.AllowAll {
			allowAllFlag = " --allow-all"
		}
	}

	baseCmd := i.Command
	if baseCmd == "" {
		baseCmd = "copilot"
	}

	// If we already have a session ID with conversation data, resume
	if i.CopilotSessionID != "" && copilotSessionHasConversationData(i.CopilotSessionID) {
		sessionLog.Info("copilot resume",
			slog.String("instance_id", i.ID),
			slog.String("session_id", i.CopilotSessionID),
			slog.String("reason", "conversation_data_present"),
		)
		return envPrefix + fmt.Sprintf(
			"%s --resume %s%s%s",
			baseCmd,
			i.CopilotSessionID,
			modelFlag,
			allowAllFlag,
		)
	}

	// Start fresh
	sessionLog.Info("copilot fresh",
		slog.String("instance_id", i.ID),
		slog.String("reason", "fresh_session"),
	)
	return envPrefix + fmt.Sprintf(
		"%s%s%s",
		baseCmd,
		modelFlag,
		allowAllFlag,
	)
}

// detectCopilotSessionAsync detects the Copilot session ID asynchronously
// after the copilot process has started. Scans ~/.copilot/session-state/
// for the most recent session matching this instance's working directory.
func (i *Instance) detectCopilotSessionAsync() {
	// Wait for copilot to initialize and write session.start
	time.Sleep(2 * time.Second)

	cwd := i.EffectiveWorkingDir()
	startedAfter := time.Now().Add(-30 * time.Second) // generous window
	if i.CopilotStartedAt > 0 {
		startedAfter = time.UnixMilli(i.CopilotStartedAt).Add(-2 * time.Second)
	}

	delays := []time.Duration{0, 2 * time.Second, 3 * time.Second}
	for attempt, delay := range delays {
		if delay > 0 {
			time.Sleep(delay)
		}

		sessionID := detectCopilotSessionFromDisk(cwd, startedAfter)
		if sessionID != "" {
			i.CopilotSessionID = sessionID
			i.CopilotDetectedAt = time.Now()

			// Propagate to tmux env for restart
			if i.tmuxSession != nil {
				if err := i.tmuxSession.SetEnvironment("COPILOT_SESSION_ID", sessionID); err != nil {
					sessionLog.Warn("copilot_set_env_failed", slog.String("error", err.Error()))
				}
			}

			sessionLog.Debug("copilot_session_detected",
				slog.String("session_id", sessionID),
				slog.Int("attempt", attempt+1),
			)
			return
		}

		sessionLog.Debug("copilot_session_not_found",
			slog.Int("attempt", attempt+1),
			slog.Int("total", len(delays)),
		)
	}

	sessionLog.Warn("copilot_detection_failed", slog.Int("attempts", len(delays)))
}

// GetCopilotOptions returns CopilotOptions from the instance's tool options.
func (i *Instance) GetCopilotOptions() *CopilotOptions {
	if len(i.ToolOptionsJSON) == 0 {
		return nil
	}
	opts, err := UnmarshalCopilotOptions(i.ToolOptionsJSON)
	if err != nil {
		return nil
	}
	return opts
}
