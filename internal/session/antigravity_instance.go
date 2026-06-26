package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

func (i *Instance) buildAntigravityCommand(baseCommand string) string {
	if i.Tool != "antigravity" {
		return baseCommand
	}

	envPrefix := i.buildEnvSourceCommand()

	yoloMode := false
	if i.AntigravityYoloMode != nil {
		yoloMode = *i.AntigravityYoloMode
	} else {
		userConfig, _ := LoadUserConfig()
		if userConfig != nil {
			yoloMode = userConfig.Antigravity.YoloMode
		}
	}

	yoloFlag := ""
	if yoloMode {
		yoloFlag = " --dangerously-skip-permissions"
	}

	modelFlag := ""
	if i.AntigravityModel != "" {
		modelFlag = " --model " + i.AntigravityModel
	} else if i.AntigravityConversationID == "" {
		userConfig, _ := LoadUserConfig()
		if userConfig != nil && userConfig.Antigravity.DefaultModel != "" {
			modelFlag = " --model " + userConfig.Antigravity.DefaultModel
		}
	}

	if baseCommand == "agy" || baseCommand == "antigravity" {
		cmd := GetToolCommand("antigravity")
		if i.AntigravityConversationID != "" {
			return envPrefix + fmt.Sprintf(
				"%s --conversation %s%s%s",
				cmd,
				i.AntigravityConversationID,
				yoloFlag,
				modelFlag,
			)
		}
		return envPrefix + fmt.Sprintf("%s%s%s", cmd, yoloFlag, modelFlag)
	}

	return envPrefix + baseCommand
}

func (i *Instance) syncAntigravityEnvToTmux() {
	if i.tmuxSession == nil {
		return
	}
	if i.AntigravityConversationID != "" {
		_ = i.tmuxSession.SetEnvironment("ANTIGRAVITY_CONVERSATION_ID", i.AntigravityConversationID)
	}
	if i.AntigravityYoloMode != nil {
		val := "false"
		if *i.AntigravityYoloMode {
			val = "true"
		}
		_ = i.tmuxSession.SetEnvironment("ANTIGRAVITY_YOLO_MODE", val)
	}
}

func (i *Instance) bindAntigravitySessionFromHook(conversationID, hookEvent string) {
	sessionLog.Debug("antigravity_session_update_from_hook",
		slog.String("old_id", i.AntigravityConversationID),
		slog.String("new_id", conversationID),
		slog.String("event", hookEvent),
	)
	i.AntigravityConversationID = conversationID
	i.AntigravityDetectedAt = time.Now()
	i.hookSessionID = conversationID

	if i.tmuxSession != nil && i.tmuxSession.Exists() {
		_ = i.tmuxSession.SetEnvironment("ANTIGRAVITY_CONVERSATION_ID", conversationID)
	}

	if db := statedb.GetGlobal(); db != nil {
		if err := db.WriteAntigravitySessionBinding(i.ID, conversationID, i.AntigravityDetectedAt); err != nil {
			sessionLog.Warn("antigravity_session_rebind_persist_failed",
				slog.String("instance_id", i.ID),
				slog.String("new_id", conversationID),
				slog.String("error", err.Error()))
		}
	}
}

func (i *Instance) UpdateAntigravitySession() {
	if i.Tool != "antigravity" {
		return
	}
	i.syncAntigravitySessionFromTmux()
	i.syncAntigravitySessionFromDisk()
	i.syncAntigravityConversationFromPane()
	i.updateAntigravityLatestPrompt()
}

func (i *Instance) syncAntigravitySessionFromTmux() {
	if i.tmuxSession == nil {
		return
	}
	if id, err := i.tmuxSession.GetEnvironment("ANTIGRAVITY_CONVERSATION_ID"); err == nil && id != "" {
		if i.AntigravityConversationID != id {
			i.AntigravityConversationID = id
		}
		i.AntigravityDetectedAt = time.Now()
	}
	if yoloEnv, err := i.tmuxSession.GetEnvironment("ANTIGRAVITY_YOLO_MODE"); err == nil && yoloEnv != "" {
		enabled := yoloEnv == "true"
		i.AntigravityYoloMode = &enabled
	}
}

func (i *Instance) syncAntigravitySessionFromDisk() {
	if i.AntigravityConversationID != "" && antigravityConversationHasData(i.AntigravityConversationID) {
		return
	}
	id := findMostRecentAntigravityConversation()
	if id == "" {
		return
	}
	if id != i.AntigravityConversationID {
		sessionLog.Debug("antigravity_session_update",
			slog.String("old_id", i.AntigravityConversationID),
			slog.String("new_id", id),
		)
	}
	i.AntigravityConversationID = id
	i.AntigravityDetectedAt = time.Now()
	if i.tmuxSession != nil && i.tmuxSession.Exists() {
		_ = i.tmuxSession.SetEnvironment("ANTIGRAVITY_CONVERSATION_ID", id)
	}
}

func (i *Instance) syncAntigravityConversationFromPane() {
	if i.tmuxSession == nil || i.AntigravityConversationID != "" {
		return
	}
	content, err := i.tmuxSession.CaptureFullHistory()
	if err != nil {
		return
	}
	if id := ExtractAntigravityConversationIDFromPane(content); id != "" {
		i.AntigravityConversationID = id
		i.AntigravityDetectedAt = time.Now()
		_ = i.tmuxSession.SetEnvironment("ANTIGRAVITY_CONVERSATION_ID", id)
	}
}

// SetAntigravityModel sets the Antigravity model for this session and restarts if running.
func (i *Instance) SetAntigravityModel(model string) error {
	i.AntigravityModel = model
	sessionLog.Debug(
		"antigravity_model_set",
		slog.String("model", model),
		slog.String("session_id", i.ID),
		slog.String("title", i.Title),
	)
	if i.Exists() {
		return i.Restart()
	}
	return nil
}

func (i *Instance) SetAntigravityYoloMode(enabled bool) {
	if i.Tool != "antigravity" {
		return
	}
	i.AntigravityYoloMode = &enabled
	if i.tmuxSession != nil && i.tmuxSession.Exists() {
		val := "false"
		if enabled {
			val = "true"
		}
		_ = i.tmuxSession.SetEnvironment("ANTIGRAVITY_YOLO_MODE", val)
	}
}

func (i *Instance) updateAntigravityLatestPrompt() {
	if i.AntigravityConversationID == "" {
		return
	}
	if prompt := parseAntigravityLatestUserPrompt(i.AntigravityConversationID); prompt != "" {
		i.LatestPrompt = prompt
	}
}

func parseAntigravityLatestUserPrompt(conversationID string) string {
	logPath := filepath.Join(GetAntigravityBrainDir(), conversationID, ".system_generated", "logs", "transcript.jsonl")
	f, err := os.Open(logPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	var lastUser string
	scanner := bufio.NewScanner(f)
	// Default 64 KiB bufio.Scanner buf truncates large USER_INPUT pastes; raise
	// to 8 MiB so multi-megabyte prompts don't silently leave lastUser stale.
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var step struct {
			Type    string `json:"type"`
			Source  string `json:"source"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(line), &step); err != nil {
			continue
		}
		if step.Type == "USER_INPUT" || step.Source == "USER_EXPLICIT" {
			lastUser = strings.TrimSpace(step.Content)
		}
	}
	return lastUser
}
