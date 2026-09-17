package session

import (
	"strings"
	"time"
)

// GeminiSessionAnalytics holds metrics for a Gemini session
type GeminiSessionAnalytics struct {
	// Token usage. Gemini CLI records six counters per message
	// ("input", "output", "cached", "thoughts", "tool", "total"); all six are
	// kept because dropping thoughts/tool understates the session and dropping
	// cached hides how much of the prompt was served from cache.
	InputTokens    int `json:"input_tokens"`
	OutputTokens   int `json:"output_tokens"`
	CachedTokens   int `json:"cached_tokens"`   // subset of InputTokens served from cache
	ThoughtsTokens int `json:"thoughts_tokens"` // reasoning tokens
	ToolTokens     int `json:"tool_tokens"`

	// ReportedTotalTokens is the sum of the per-message "total" fields, i.e. the
	// harness's own accounting. Preferred over any sum computed here.
	ReportedTotalTokens int `json:"reported_total_tokens"`

	// Current context size: the last turn's input tokens, which for Gemini
	// already include the cached portion and the full prior history.
	CurrentContextTokens int `json:"current_context_tokens"`

	// CurrentContextCachedTokens is how much of CurrentContextTokens was served
	// from cache on that same turn.
	CurrentContextCachedTokens int `json:"current_context_cached_tokens"`

	// Session metrics
	TotalTurns int           `json:"total_turns"`
	Duration   time.Duration `json:"duration"`
	StartTime  time.Time     `json:"start_time"`
	LastActive time.Time     `json:"last_active"`

	// Cost estimation
	EstimatedCost float64 `json:"estimated_cost"`

	// Model detected from session file messages
	Model string `json:"model,omitempty"`

	// In-memory cache: last file modification time (skip re-parse if unchanged)
	LastFileModTime time.Time `json:"-"`
}

// TotalTokens returns the session's total token count. Gemini reports a "total"
// per message (input + output + thoughts + tool), so prefer that measured value
// and only fall back to summing the parts when the session file carried none
// (older Gemini CLI writes, or a file with no gemini-typed messages).
// CachedTokens is deliberately excluded: it is a subset of InputTokens.
func (a *GeminiSessionAnalytics) TotalTokens() int {
	if a.ReportedTotalTokens > 0 {
		return a.ReportedTotalTokens
	}
	return a.InputTokens + a.OutputTokens + a.ThoughtsTokens + a.ToolTokens
}

// geminiModelContextWindowPrefixes maps Gemini model ID prefixes to context
// window sizes. Ordered most-specific first, matching the Claude table in
// analytics.go, so "gemini-1.5-pro" resolves before any "gemini-1.5" entry.
var geminiModelContextWindowPrefixes = []struct {
	prefix string
	size   int
}{
	{"gemini-1.5-pro", 2000000},
	{"gemini-1.5-flash", 1000000},
	{"gemini-2.0-flash", 1000000},
	{"gemini-2.5-flash", 1000000},
	{"gemini-2.5-pro", 1000000},
	{"gemini-3", 1000000},
}

// geminiDefaultContextWindow is used when the model ID is empty or unrecognised.
const geminiDefaultContextWindow = 1000000

// GeminiContextWindowForModel returns the context window size for a Gemini model
// ID. Returns on the first prefix match; falls back to
// geminiDefaultContextWindow for unknown or empty model IDs.
func GeminiContextWindowForModel(model string) int {
	for _, entry := range geminiModelContextWindowPrefixes {
		if strings.HasPrefix(model, entry.prefix) {
			return entry.size
		}
	}
	return geminiDefaultContextWindow
}

// ContextPercent returns the percentage of the context window in use.
// modelLimit is the model's context window size; if 0 it is resolved from the
// Model field rather than assuming a single hardcoded window for every model.
func (a *GeminiSessionAnalytics) ContextPercent(modelLimit int) float64 {
	if modelLimit == 0 {
		modelLimit = GeminiContextWindowForModel(a.Model)
	}
	if modelLimit <= 0 {
		return 0
	}
	return float64(a.CurrentContextTokens) / float64(modelLimit) * 100
}

// GeminiModelPricing holds pricing per million tokens
type GeminiModelPricing struct {
	Input  float64
	Output float64
}

// geminiPricing contains pricing per million tokens for each model (as of Jan 2025)
var geminiPricing = map[string]GeminiModelPricing{
	"gemini-1.5-flash": {Input: 0.075, Output: 0.30},
	"gemini-1.5-pro":   {Input: 3.50, Output: 10.50},
	"gemini-2.0-flash": {Input: 0.10, Output: 0.40},
	"gemini-2.5-flash": {Input: 0.15, Output: 0.60},
	"gemini-2.5-pro":   {Input: 1.25, Output: 10.00},
	// Fallback
	"default": {Input: 0.15, Output: 0.60},
}

// CalculateCost estimates session cost based on token usage and model pricing
func (a *GeminiSessionAnalytics) CalculateCost(model string) float64 {
	pricing, ok := geminiPricing[model]
	if !ok {
		pricing = geminiPricing["default"]
	}

	inputM := float64(a.InputTokens) / 1_000_000
	outputM := float64(a.OutputTokens) / 1_000_000

	return inputM*pricing.Input + outputM*pricing.Output
}
