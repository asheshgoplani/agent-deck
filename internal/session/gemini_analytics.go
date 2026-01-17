package session

import (
	"time"
)

// GeminiSessionAnalytics holds metrics for a Gemini session
type GeminiSessionAnalytics struct {
	// Token usage
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`

	// Session metrics
	TotalTurns int           `json:"total_turns"`
	Duration   time.Duration `json:"duration"`
	StartTime  time.Time     `json:"start_time"`
	LastActive time.Time     `json:"last_active"`

	// Cost estimation
	EstimatedCost float64 `json:"estimated_cost"`
}

// TotalTokens returns the sum of input and output tokens
func (a *GeminiSessionAnalytics) TotalTokens() int {
	return a.InputTokens + a.OutputTokens
}
