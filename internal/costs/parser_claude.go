package costs

import (
	"encoding/json"
)

// ClaudeHookParser parses Claude Code Stop hook JSON payloads.
type ClaudeHookParser struct {
	pricer *Pricer
}

type claudeHookPayload struct {
	HookEventName string `json:"hook_event_name"`
	SessionID     string `json:"session_id"`
	Source        string `json:"source"`
	Result        struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"result"`
}

func (p *ClaudeHookParser) Name() string { return "claude" }

func (p *ClaudeHookParser) CanParse(toolType string) bool {
	return toolType == "claude"
}

func (p *ClaudeHookParser) Parse(input string) ([]CostEvent, error) {
	var payload claudeHookPayload
	if err := json.Unmarshal([]byte(input), &payload); err != nil {
		return nil, err
	}
	if payload.HookEventName != "Stop" {
		return nil, nil
	}
	usage := payload.Result.Usage
	if usage.InputTokens == 0 && usage.OutputTokens == 0 {
		return nil, nil
	}
	ev := CostEvent{
		Model:            payload.Result.Model,
		InputTokens:      usage.InputTokens,
		OutputTokens:     usage.OutputTokens,
		CacheReadTokens:  usage.CacheReadInputTokens,
		CacheWriteTokens: usage.CacheCreationInputTokens,
	}
	return []CostEvent{ev}, nil
}
