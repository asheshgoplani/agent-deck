package session

import (
	"bufio"
	"encoding/json"
	"os"
	"time"
)

// SessionAnalytics holds parsed session metrics from Claude JSONL files
type SessionAnalytics struct {
	// Token usage
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_input_tokens"`
	CacheWriteTokens int `json:"cache_creation_input_tokens"`

	// Session metrics
	TotalTurns int           `json:"total_turns"`
	Duration   time.Duration `json:"duration"`
	StartTime  time.Time     `json:"start_time"`
	LastActive time.Time     `json:"last_active"`

	// Tool usage
	ToolCalls []ToolCall `json:"tool_calls"`

	// Subagents
	Subagents []SubagentInfo `json:"subagents"`

	// Cost estimation
	EstimatedCost float64 `json:"estimated_cost"`

	// 5-hour billing blocks
	BillingBlocks []BillingBlock `json:"billing_blocks"`
}

// ToolCall represents a tool and its usage count
type ToolCall struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// SubagentInfo holds metadata about a subagent spawned during a session
type SubagentInfo struct {
	ID        string    `json:"id"`
	StartTime time.Time `json:"start_time"`
	Turns     int       `json:"turns"`
}

// BillingBlock represents a 5-hour billing window
type BillingBlock struct {
	StartTime  time.Time `json:"start_time"`
	EndTime    time.Time `json:"end_time"`
	TokensUsed int       `json:"tokens_used"`
	IsActive   bool      `json:"is_active"`
}

// TotalTokens returns the sum of all token types
func (a *SessionAnalytics) TotalTokens() int {
	return a.InputTokens + a.OutputTokens + a.CacheReadTokens + a.CacheWriteTokens
}

// ContextPercent returns the percentage of context window used
// modelLimit is the model's context window size (defaults to 200000 for Claude)
func (a *SessionAnalytics) ContextPercent(modelLimit int) float64 {
	if modelLimit == 0 {
		modelLimit = 200000 // Default Claude limit
	}
	return float64(a.TotalTokens()) / float64(modelLimit) * 100
}

// jsonlEntry represents a single line in a Claude session JSONL file
type jsonlEntry struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Message   struct {
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
		Content []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"content"`
	} `json:"message"`
	AgentID string `json:"agent_id,omitempty"`
}

// ParseSessionJSONL parses a Claude session JSONL file and returns analytics
func ParseSessionJSONL(path string) (*SessionAnalytics, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	analytics := &SessionAnalytics{
		ToolCalls: []ToolCall{},
	}
	toolCounts := make(map[string]int)
	var firstTime, lastTime time.Time

	scanner := bufio.NewScanner(file)
	// Increase buffer for large lines (some tool outputs can be huge)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		var entry jsonlEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue // Skip malformed lines
		}

		// Only count assistant messages
		if entry.Type != "assistant" {
			continue
		}

		// Track timing
		if !entry.Timestamp.IsZero() {
			if firstTime.IsZero() || entry.Timestamp.Before(firstTime) {
				firstTime = entry.Timestamp
			}
			if entry.Timestamp.After(lastTime) {
				lastTime = entry.Timestamp
			}
		}

		// Accumulate tokens
		analytics.InputTokens += entry.Message.Usage.InputTokens
		analytics.OutputTokens += entry.Message.Usage.OutputTokens
		analytics.CacheReadTokens += entry.Message.Usage.CacheReadInputTokens
		analytics.CacheWriteTokens += entry.Message.Usage.CacheCreationInputTokens

		// Count turn
		analytics.TotalTurns++

		// Count tool calls
		for _, content := range entry.Message.Content {
			if content.Type == "tool_use" && content.Name != "" {
				toolCounts[content.Name]++
			}
		}
	}

	// Convert tool counts to slice
	for name, count := range toolCounts {
		analytics.ToolCalls = append(analytics.ToolCalls, ToolCall{
			Name:  name,
			Count: count,
		})
	}

	// Set timing
	analytics.StartTime = firstTime
	analytics.LastActive = lastTime
	if !firstTime.IsZero() && !lastTime.IsZero() {
		analytics.Duration = lastTime.Sub(firstTime)
	}

	return analytics, scanner.Err()
}
