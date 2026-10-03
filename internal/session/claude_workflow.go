package session

import (
	"encoding/json"
	"strings"
)

// scanClaudeTranscriptBackgroundWork reads the transcript tail to check whether
// an asynchronous background Workflow or background task is in flight.
func scanClaudeTranscriptBackgroundWork(path string) (bool, string) {
	if strings.TrimSpace(path) == "" {
		return false, ""
	}
	lines, err := TranscriptTailLines(path, turnScanTailLines)
	if err != nil || len(lines) == 0 {
		return false, ""
	}
	return classifyTranscriptBackgroundWork(lines)
}

type transcriptToolUseBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type transcriptWorkflowInput struct {
	Name string `json:"name"`
}

// classifyTranscriptBackgroundWork inspects transcript lines to detect if a
// Workflow or background task was launched and has not finished yet.
func classifyTranscriptBackgroundWork(lines []string) (bool, string) {
	var taskName string
	sawCompleted := false
	sawWorkflow := false

	for i := len(lines) - 1; i >= 0; i-- {
		var rec transcriptTurnRecord
		if err := json.Unmarshal([]byte(lines[i]), &rec); err != nil || rec.IsSidechain {
			continue
		}
		switch rec.Type {
		case "user":
			text := strings.TrimSpace(transcriptText(rec.Message.Content))
			if rec.TurnOrigin == "task_notification" || rec.Origin.Kind == "task-notification" ||
				strings.Contains(text, "<task-notification>") {
				if strings.Contains(text, "<status>completed</status>") ||
					strings.Contains(text, "<status>failed</status>") ||
					strings.Contains(text, "<status>stopped</status>") {
					sawCompleted = true
				}
			}
		case "assistant":
			var blocks []transcriptToolUseBlock
			if err := json.Unmarshal(rec.Message.Content, &blocks); err == nil {
				for _, b := range blocks {
					if b.Type == "tool_use" && strings.EqualFold(b.Name, "Workflow") {
						sawWorkflow = true
						var input transcriptWorkflowInput
						if err := json.Unmarshal(b.Input, &input); err == nil && input.Name != "" {
							taskName = input.Name
						}
						// If a completion landed after this workflow tool call, it's finished.
						if sawCompleted {
							return false, ""
						}
						return true, taskName
					}
				}
			}
			// Check if assistant sent a completion sentinel
			text := strings.TrimSpace(transcriptText(rec.Message.Content))
			if _, hasDone := ScanDoneSentinel(text); hasDone && sawCompleted {
				return false, ""
			}
		}
	}

	if sawWorkflow && !sawCompleted {
		return true, taskName
	}
	return false, ""
}
