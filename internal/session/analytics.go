package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"time"
)

// SessionAnalytics holds parsed session metrics from Claude JSONL files
type SessionAnalytics struct {
	// Token usage (cumulative across all turns)
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_input_tokens"`
	CacheWriteTokens int `json:"cache_creation_input_tokens"`

	// Current context size: the last turn's input_tokens +
	// cache_creation_input_tokens + cache_read_input_tokens. All three are
	// prompt-side tokens, so all three occupy the context window. Omitting
	// cache_creation makes a cache-write turn (input=6, cache_creation=125207,
	// cache_read=0) report a 6-token context. This is the same sum Claude Code's
	// own /context uses.
	CurrentContextTokens int `json:"current_context_tokens"`

	// Session metrics
	TotalTurns int           `json:"total_turns"`
	Duration   time.Duration `json:"duration"`
	StartTime  time.Time     `json:"start_time"`
	LastActive time.Time     `json:"last_active"`

	// Tool usage
	ToolCalls []ToolCall `json:"tool_calls"`

	// Model ID from the last assistant message (e.g. "claude-opus-4-6")
	Model string `json:"model,omitempty"`

	// Subagents
	Subagents []SubagentInfo `json:"subagents"`

	// Cost estimation
	EstimatedCost float64 `json:"estimated_cost"`

	// 5-hour billing blocks
	BillingBlocks []BillingBlock `json:"billing_blocks"`

	// ParseGaps counts transcript lines that were read but could not be
	// interpreted (malformed JSON, or a line longer than the read limit).
	// A non-zero value means every number above may be stale: the skipped line
	// can be the most recent usage record. Callers must surface this rather
	// than presenting the totals as complete.
	ParseGaps int `json:"parse_gaps"`

	// ParseGapSample holds the first maxParseGapSamples gaps, for diagnostics.
	ParseGapSample []ParseGap `json:"parse_gap_sample,omitempty"`
}

// ParseGap records a transcript line that could not be parsed.
type ParseGap struct {
	Line   int    `json:"line"`   // 1-indexed line number within the transcript
	Reason string `json:"reason"` // ParseGapMalformed | ParseGapOversize
	Bytes  int    `json:"bytes"`  // byte length of the offending line
}

// Parse gap reasons.
const (
	ParseGapMalformed = "malformed json"
	ParseGapOversize  = "line exceeds read limit"
)

// HasParseGaps reports whether any transcript line was skipped, i.e. whether
// the totals should be labelled as possibly stale.
func (a *SessionAnalytics) HasParseGaps() bool {
	return a != nil && a.ParseGaps > 0
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

// modelContextWindow maps model ID prefixes to their context window sizes.
// More specific prefixes must come first to ensure correct matching
// (e.g. "claude-sonnet-4-6" before "claude-sonnet-4").
var modelContextWindowPrefixes = []struct {
	prefix string
	size   int
}{
	// Claude 5 family: 1M context
	{"claude-fable-5", 1_000_000},
	{"claude-mythos-5", 1_000_000},
	{"claude-opus-5", 1_000_000},
	{"claude-sonnet-5", 1_000_000},
	// 4.8 models: 1M context (must precede 4.x fallback)
	{"claude-opus-4-8", 1_000_000},
	// 4.7 models: 1M context (must precede 4.x fallback)
	{"claude-opus-4-7", 1000000},
	// 4.6 models: 1M context
	{"claude-opus-4-6", 1000000},
	{"claude-sonnet-4-6", 1000000},
	// 4.x models (non-4.6/4.7): 200k context
	{"claude-opus-4", 200000},
	{"claude-sonnet-4", 200000},
	{"claude-haiku-4", 200000},
	// 3.x models: 200k context
	{"claude-3-5", 200000},
	{"claude-3-opus", 200000},
	// MiniMax models
	{"MiniMax-M3", 1000000},            // 1M context
	{"MiniMax-M2.7", 204800},           // 204.8K context
	{"MiniMax-M2.5-highspeed", 204000}, // 204K context (must precede M2.5)
	{"MiniMax-M2.5", 204000},           // 204K context
}

// contextWindowForModel returns the context window size for a model ID.
// Returns on first prefix match; entries are ordered most-specific first
// (e.g. "claude-sonnet-4-6" before "claude-sonnet-4") to ensure correct resolution.
func contextWindowForModel(model string) int {
	for _, entry := range modelContextWindowPrefixes {
		if len(model) >= len(entry.prefix) && model[:len(entry.prefix)] == entry.prefix {
			return entry.size
		}
	}
	return 200000 // Default Claude limit
}

// ContextWindowForModel is the exported form of contextWindowForModel, so other
// packages resolve a window from the same table instead of hardcoding a number.
func ContextWindowForModel(model string) int {
	return contextWindowForModel(model)
}

// ContextPercent returns the percentage of context window used
// Uses CurrentContextTokens (last turn's input + cache) for accurate context usage
// modelLimit is the model's context window size; if 0, it is inferred from the Model field
func (a *SessionAnalytics) ContextPercent(modelLimit int) float64 {
	if modelLimit == 0 {
		modelLimit = contextWindowForModel(a.Model)
	}
	return float64(a.CurrentContextTokens) / float64(modelLimit) * 100
}

// ModelPricing holds pricing per million tokens for a model
type ModelPricing struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
}

// modelPricing contains pricing per million tokens for each model (as of Jan 2025)
var modelPricing = map[string]ModelPricing{
	"claude-sonnet-4-20250514": {Input: 3.0, Output: 15.0, CacheRead: 0.30, CacheWrite: 3.75},
	"claude-opus-4-20250514":   {Input: 15.0, Output: 75.0, CacheRead: 1.50, CacheWrite: 18.75},
	"claude-3-5-sonnet":        {Input: 3.0, Output: 15.0, CacheRead: 0.30, CacheWrite: 3.75},
	"claude-3-5-haiku":         {Input: 0.80, Output: 4.0, CacheRead: 0.08, CacheWrite: 1.0},
	// MiniMax models
	"MiniMax-M3":             {Input: 0.60, Output: 2.40, CacheRead: 0.12},
	"MiniMax-M2.7":           {Input: 0.30, Output: 1.20, CacheRead: 0.06, CacheWrite: 0.375},
	"MiniMax-M2.7-highspeed": {Input: 0.35, Output: 1.40},
	"MiniMax-M2.5":           {Input: 0.50, Output: 2.00},
	"MiniMax-M2.5-highspeed": {Input: 0.15, Output: 0.60},
	// Default fallback uses Sonnet pricing
	"default": {Input: 3.0, Output: 15.0, CacheRead: 0.30, CacheWrite: 3.75},
}

// CalculateCost estimates session cost based on token usage and model pricing
func (a *SessionAnalytics) CalculateCost(model string) float64 {
	pricing, ok := modelPricing[model]
	if !ok {
		pricing = modelPricing["default"]
	}

	// Convert to millions
	inputM := float64(a.InputTokens) / 1_000_000
	outputM := float64(a.OutputTokens) / 1_000_000
	cacheReadM := float64(a.CacheReadTokens) / 1_000_000
	cacheWriteM := float64(a.CacheWriteTokens) / 1_000_000

	return inputM*pricing.Input +
		outputM*pricing.Output +
		cacheReadM*pricing.CacheRead +
		cacheWriteM*pricing.CacheWrite
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
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"content"`
	} `json:"message"`
	AgentID string `json:"agent_id,omitempty"`
}

const (
	// maxParseGapSamples caps the per-file gap detail retained for diagnostics.
	maxParseGapSamples = 20

	// transcriptReaderBufBytes is the read-ahead buffer for the line reader.
	transcriptReaderBufBytes = 256 * 1024
)

// maxTranscriptLineBytes caps how much of a single JSONL line is buffered.
// A longer line is skipped and recorded as a ParseGap instead of aborting the
// read: bufio.Scanner reports ErrTooLong and stops, which would silently discard
// every record after the oversize line — including the newest usage record.
// A var (never mutated at runtime) so tests can shrink it, as geminiConfigDirOverride does.
var maxTranscriptLineBytes = 16 * 1024 * 1024

// readTranscriptLine reads one newline-terminated line from r, buffering at most
// limit bytes of it. When the line is longer than limit its remainder is drained
// and discarded and tooLong is true; size is the full byte length of the line
// either way. The returned error is io.EOF once the reader is exhausted.
func readTranscriptLine(r *bufio.Reader, limit int) (line []byte, size int, tooLong bool, err error) {
	for {
		chunk, readErr := r.ReadSlice('\n')
		size += len(chunk)
		if !tooLong {
			if len(line)+len(chunk) > limit {
				tooLong = true
				line = nil
			} else {
				line = append(line, chunk...)
			}
		}
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue // partial line; keep draining until the newline
		}
		return line, size, tooLong, readErr
	}
}

// recordParseGap notes a transcript line that could not be interpreted.
func (a *SessionAnalytics) recordParseGap(line int, reason string, size int) {
	a.ParseGaps++
	if len(a.ParseGapSample) < maxParseGapSamples {
		a.ParseGapSample = append(a.ParseGapSample, ParseGap{
			Line:   line,
			Reason: reason,
			Bytes:  size,
		})
	}
}

// ParseSessionJSONL parses a Claude session JSONL file and returns analytics.
// Lines that cannot be parsed are counted in SessionAnalytics.ParseGaps rather
// than silently dropped, so callers can label the totals as possibly stale.
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

	apply := func(entry jsonlEntry) {
		// Only count assistant messages
		if entry.Type != "assistant" {
			return
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

		// Track model ID (use last seen non-synthetic model)
		if entry.Message.Model != "" && entry.Message.Model != "<synthetic>" {
			analytics.Model = entry.Message.Model
		}

		usage := entry.Message.Usage

		// Accumulate tokens (cumulative totals for cost calculation)
		analytics.InputTokens += usage.InputTokens
		analytics.OutputTokens += usage.OutputTokens
		analytics.CacheReadTokens += usage.CacheReadInputTokens
		analytics.CacheWriteTokens += usage.CacheCreationInputTokens

		// Track current context size from the last record that actually carries
		// usage. All three prompt-side counters occupy the context window; a
		// cache-write turn puts nearly all of them in cache_creation, so leaving
		// it out reports a near-empty context. Records with no usage at all
		// (synthetic/error assistant messages) must not reset the number to 0.
		if prompt := usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens; prompt > 0 {
			analytics.CurrentContextTokens = prompt
		}

		// Count turn
		analytics.TotalTurns++

		// Count tool calls
		for _, content := range entry.Message.Content {
			if content.Type == "tool_use" && content.Name != "" {
				toolCounts[content.Name]++
			}
		}
	}

	reader := bufio.NewReaderSize(file, transcriptReaderBufBytes)
	lineNo := 0
	for {
		raw, size, tooLong, readErr := readTranscriptLine(reader, maxTranscriptLineBytes)
		if size > 0 {
			lineNo++
			trimmed := bytes.TrimSpace(raw)
			switch {
			case tooLong:
				analytics.recordParseGap(lineNo, ParseGapOversize, size)
			case len(trimmed) == 0:
				// Blank separator line, not a gap.
			default:
				var entry jsonlEntry
				if err := json.Unmarshal(trimmed, &entry); err != nil {
					analytics.recordParseGap(lineNo, ParseGapMalformed, size)
				} else {
					apply(entry)
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return analytics, readErr
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

	return analytics, nil
}

// CalculateBillingBlocks groups timestamps into billing windows.
// Claude Code API bills in 5-hour windows. Each block represents a billing period.
// Timestamps are sorted chronologically and grouped - a new block starts when
// a timestamp exceeds the windowSize from the current block's start time.
// The last block is marked as "active" if it's still within the window from now.
func CalculateBillingBlocks(timestamps []time.Time, windowSize time.Duration) []BillingBlock {
	if len(timestamps) == 0 {
		return nil
	}

	// Sort timestamps chronologically
	sorted := make([]time.Time, len(timestamps))
	copy(sorted, timestamps)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Before(sorted[j])
	})

	var blocks []BillingBlock
	var currentBlock *BillingBlock

	for _, ts := range sorted {
		if currentBlock == nil || ts.Sub(currentBlock.StartTime) >= windowSize {
			// Start new block
			if currentBlock != nil {
				blocks = append(blocks, *currentBlock)
			}
			currentBlock = &BillingBlock{
				StartTime: ts,
				EndTime:   ts,
			}
		} else {
			// Extend current block
			currentBlock.EndTime = ts
		}
	}

	// Append the final block
	if currentBlock != nil {
		blocks = append(blocks, *currentBlock)
	}

	// Mark current block as active if within window from now
	now := time.Now()
	if len(blocks) > 0 {
		lastBlock := &blocks[len(blocks)-1]
		if now.Sub(lastBlock.StartTime) < windowSize {
			lastBlock.IsActive = true
		}
	}

	return blocks
}
