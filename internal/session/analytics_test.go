package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionAnalytics_TotalTokens(t *testing.T) {
	analytics := &SessionAnalytics{
		InputTokens:      1000,
		OutputTokens:     500,
		CacheReadTokens:  200,
		CacheWriteTokens: 100,
		TotalTurns:       5,
		ToolCalls:        []ToolCall{{Name: "Read", Count: 3}},
		Duration:         time.Hour,
		StartTime:        time.Now().Add(-time.Hour),
	}

	assert.Equal(t, 1800, analytics.TotalTokens())
}

func TestSessionAnalytics_ContextPercent(t *testing.T) {
	analytics := &SessionAnalytics{
		InputTokens:          1000,
		OutputTokens:         500,
		CacheReadTokens:      200,
		CacheWriteTokens:     100,
		CurrentContextTokens: 1800, // Last turn's context size
	}

	// CurrentContextTokens 1800 / 200000 limit * 100 = 0.9%
	assert.InDelta(t, 0.9, analytics.ContextPercent(200000), 0.01)
}

func TestSessionAnalytics_ContextPercent_DefaultLimit(t *testing.T) {
	analytics := &SessionAnalytics{
		InputTokens:          20000,
		OutputTokens:         0,
		CurrentContextTokens: 20000, // Last turn's context size
	}

	// CurrentContextTokens 20000 / 200000 default limit * 100 = 10%
	assert.InDelta(t, 10.0, analytics.ContextPercent(0), 0.01)
}

func TestSessionAnalytics_ZeroTokens(t *testing.T) {
	analytics := &SessionAnalytics{}

	assert.Equal(t, 0, analytics.TotalTokens())
	assert.InDelta(t, 0.0, analytics.ContextPercent(200000), 0.01)
}

func TestToolCall(t *testing.T) {
	tc := ToolCall{
		Name:  "Read",
		Count: 5,
	}

	assert.Equal(t, "Read", tc.Name)
	assert.Equal(t, 5, tc.Count)
}

func TestSubagentInfo(t *testing.T) {
	now := time.Now()
	sa := SubagentInfo{
		ID:        "subagent-123",
		StartTime: now,
		Turns:     10,
	}

	assert.Equal(t, "subagent-123", sa.ID)
	assert.Equal(t, now, sa.StartTime)
	assert.Equal(t, 10, sa.Turns)
}

func TestBillingBlock(t *testing.T) {
	start := time.Now()
	end := start.Add(5 * time.Hour)

	bb := BillingBlock{
		StartTime:  start,
		EndTime:    end,
		TokensUsed: 50000,
		IsActive:   true,
	}

	assert.Equal(t, start, bb.StartTime)
	assert.Equal(t, end, bb.EndTime)
	assert.Equal(t, 50000, bb.TokensUsed)
	assert.True(t, bb.IsActive)
}

func TestParseJSONL(t *testing.T) {
	// Create temp JSONL file
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "session.jsonl")

	jsonl := `{"type":"assistant","message":{"usage":{"input_tokens":100,"output_tokens":50}}}
{"type":"assistant","message":{"usage":{"input_tokens":200,"output_tokens":100},"content":[{"type":"tool_use","name":"Read"}]}}
{"type":"assistant","message":{"usage":{"input_tokens":150,"output_tokens":75},"content":[{"type":"tool_use","name":"Read"},{"type":"tool_use","name":"Edit"}]}}`

	err := os.WriteFile(jsonlPath, []byte(jsonl), 0644)
	require.NoError(t, err)

	analytics, err := ParseSessionJSONL(jsonlPath)
	require.NoError(t, err)

	assert.Equal(t, 450, analytics.InputTokens)
	assert.Equal(t, 225, analytics.OutputTokens)
	assert.Equal(t, 3, analytics.TotalTurns)

	// Check tool calls
	readCalls := 0
	editCalls := 0
	for _, tc := range analytics.ToolCalls {
		if tc.Name == "Read" {
			readCalls = tc.Count
		}
		if tc.Name == "Edit" {
			editCalls = tc.Count
		}
	}
	assert.Equal(t, 2, readCalls)
	assert.Equal(t, 1, editCalls)
}

func TestParseJSONL_WithTimestamps(t *testing.T) {
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "session.jsonl")

	// JSONL with timestamps
	jsonl := `{"type":"assistant","timestamp":"2025-01-10T10:00:00Z","message":{"usage":{"input_tokens":100,"output_tokens":50}}}
{"type":"assistant","timestamp":"2025-01-10T10:05:00Z","message":{"usage":{"input_tokens":200,"output_tokens":100}}}
{"type":"assistant","timestamp":"2025-01-10T10:10:00Z","message":{"usage":{"input_tokens":150,"output_tokens":75}}}`

	err := os.WriteFile(jsonlPath, []byte(jsonl), 0644)
	require.NoError(t, err)

	analytics, err := ParseSessionJSONL(jsonlPath)
	require.NoError(t, err)

	// Check timing
	expectedStart := time.Date(2025, 1, 10, 10, 0, 0, 0, time.UTC)
	expectedEnd := time.Date(2025, 1, 10, 10, 10, 0, 0, time.UTC)

	assert.Equal(t, expectedStart, analytics.StartTime)
	assert.Equal(t, expectedEnd, analytics.LastActive)
	assert.Equal(t, 10*time.Minute, analytics.Duration)
}

func TestParseJSONL_WithCacheTokens(t *testing.T) {
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "session.jsonl")

	jsonl := `{"type":"assistant","message":{"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":500,"cache_creation_input_tokens":200}}}`

	err := os.WriteFile(jsonlPath, []byte(jsonl), 0644)
	require.NoError(t, err)

	analytics, err := ParseSessionJSONL(jsonlPath)
	require.NoError(t, err)

	assert.Equal(t, 100, analytics.InputTokens)
	assert.Equal(t, 50, analytics.OutputTokens)
	assert.Equal(t, 500, analytics.CacheReadTokens)
	assert.Equal(t, 200, analytics.CacheWriteTokens)
}

func TestParseJSONL_SkipNonAssistant(t *testing.T) {
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "session.jsonl")

	// Mix of types - only assistant should be counted
	jsonl := `{"type":"user","message":{"content":"hello"}}
{"type":"assistant","message":{"usage":{"input_tokens":100,"output_tokens":50}}}
{"type":"system","message":{"content":"You are helpful"}}
{"type":"assistant","message":{"usage":{"input_tokens":200,"output_tokens":100}}}`

	err := os.WriteFile(jsonlPath, []byte(jsonl), 0644)
	require.NoError(t, err)

	analytics, err := ParseSessionJSONL(jsonlPath)
	require.NoError(t, err)

	assert.Equal(t, 300, analytics.InputTokens)
	assert.Equal(t, 150, analytics.OutputTokens)
	assert.Equal(t, 2, analytics.TotalTurns) // Only assistant messages
}

func TestParseJSONL_SkipMalformedLines(t *testing.T) {
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "session.jsonl")

	// Contains malformed JSON that should be skipped
	jsonl := `{"type":"assistant","message":{"usage":{"input_tokens":100,"output_tokens":50}}}
this is not valid json
{"broken json
{"type":"assistant","message":{"usage":{"input_tokens":200,"output_tokens":100}}}`

	err := os.WriteFile(jsonlPath, []byte(jsonl), 0644)
	require.NoError(t, err)

	analytics, err := ParseSessionJSONL(jsonlPath)
	require.NoError(t, err)

	// Should only count the 2 valid assistant messages
	assert.Equal(t, 300, analytics.InputTokens)
	assert.Equal(t, 150, analytics.OutputTokens)
	assert.Equal(t, 2, analytics.TotalTurns)
}

func TestParseJSONL_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "session.jsonl")

	err := os.WriteFile(jsonlPath, []byte(""), 0644)
	require.NoError(t, err)

	analytics, err := ParseSessionJSONL(jsonlPath)
	require.NoError(t, err)

	assert.Equal(t, 0, analytics.InputTokens)
	assert.Equal(t, 0, analytics.OutputTokens)
	assert.Equal(t, 0, analytics.TotalTurns)
	assert.Empty(t, analytics.ToolCalls)
}

func TestParseJSONL_FileNotFound(t *testing.T) {
	_, err := ParseSessionJSONL("/nonexistent/path/session.jsonl")
	assert.Error(t, err)
}

func TestParseJSONL_MultipleToolCalls(t *testing.T) {
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "session.jsonl")

	// Various tool calls across multiple messages
	jsonl := `{"type":"assistant","message":{"usage":{"input_tokens":100,"output_tokens":50},"content":[{"type":"tool_use","name":"Read"},{"type":"tool_use","name":"Read"},{"type":"tool_use","name":"Read"}]}}
{"type":"assistant","message":{"usage":{"input_tokens":100,"output_tokens":50},"content":[{"type":"tool_use","name":"Edit"},{"type":"tool_use","name":"Bash"}]}}
{"type":"assistant","message":{"usage":{"input_tokens":100,"output_tokens":50},"content":[{"type":"tool_use","name":"Read"},{"type":"tool_use","name":"Write"}]}}`

	err := os.WriteFile(jsonlPath, []byte(jsonl), 0644)
	require.NoError(t, err)

	analytics, err := ParseSessionJSONL(jsonlPath)
	require.NoError(t, err)

	// Build a map for easier checking
	toolMap := make(map[string]int)
	for _, tc := range analytics.ToolCalls {
		toolMap[tc.Name] = tc.Count
	}

	assert.Equal(t, 4, toolMap["Read"])
	assert.Equal(t, 1, toolMap["Edit"])
	assert.Equal(t, 1, toolMap["Bash"])
	assert.Equal(t, 1, toolMap["Write"])
}

func TestCostCalculation(t *testing.T) {
	analytics := &SessionAnalytics{
		InputTokens:      1000000, // 1M input tokens
		OutputTokens:     100000,  // 100K output tokens
		CacheReadTokens:  500000,  // 500K cache read
		CacheWriteTokens: 200000,  // 200K cache write
	}

	cost := analytics.CalculateCost("claude-sonnet-4-20250514")

	// Sonnet pricing: $3/MTok input, $15/MTok output, $0.30/MTok cache read, $3.75/MTok cache write
	// Expected: (1M * 3 + 100K * 15 + 500K * 0.30 + 200K * 3.75) / 1M
	// = (3 + 1.5 + 0.15 + 0.75) = $5.40
	assert.InDelta(t, 5.40, cost, 0.01)
}

func TestCostCalculation_OpusModel(t *testing.T) {
	analytics := &SessionAnalytics{
		InputTokens:      1000000, // 1M input tokens
		OutputTokens:     100000,  // 100K output tokens
		CacheReadTokens:  500000,  // 500K cache read
		CacheWriteTokens: 200000,  // 200K cache write
	}

	cost := analytics.CalculateCost("claude-opus-4-20250514")

	// Opus pricing: $15/MTok input, $75/MTok output, $1.50/MTok cache read, $18.75/MTok cache write
	// Expected: (1M * 15 + 100K * 75 + 500K * 1.50 + 200K * 18.75) / 1M
	// = (15 + 7.5 + 0.75 + 3.75) = $27.00
	assert.InDelta(t, 27.00, cost, 0.01)
}

func TestCostCalculation_HaikuModel(t *testing.T) {
	analytics := &SessionAnalytics{
		InputTokens:      1000000, // 1M input tokens
		OutputTokens:     100000,  // 100K output tokens
		CacheReadTokens:  500000,  // 500K cache read
		CacheWriteTokens: 200000,  // 200K cache write
	}

	cost := analytics.CalculateCost("claude-3-5-haiku")

	// Haiku pricing: $0.80/MTok input, $4/MTok output, $0.08/MTok cache read, $1.0/MTok cache write
	// Expected: (1M * 0.80 + 100K * 4 + 500K * 0.08 + 200K * 1.0) / 1M
	// = (0.80 + 0.4 + 0.04 + 0.2) = $1.44
	assert.InDelta(t, 1.44, cost, 0.01)
}

func TestCostCalculation_DefaultFallback(t *testing.T) {
	analytics := &SessionAnalytics{
		InputTokens:      1000000, // 1M input tokens
		OutputTokens:     100000,  // 100K output tokens
		CacheReadTokens:  500000,  // 500K cache read
		CacheWriteTokens: 200000,  // 200K cache write
	}

	// Unknown model should use default (Sonnet) pricing
	cost := analytics.CalculateCost("unknown-model-xyz")

	// Same as Sonnet: $5.40
	assert.InDelta(t, 5.40, cost, 0.01)
}

func TestCostCalculation_ZeroTokens(t *testing.T) {
	analytics := &SessionAnalytics{}

	cost := analytics.CalculateCost("claude-sonnet-4-20250514")

	assert.Equal(t, 0.0, cost)
}

func TestCostCalculation_Sonnet35(t *testing.T) {
	analytics := &SessionAnalytics{
		InputTokens:      1000000, // 1M input tokens
		OutputTokens:     100000,  // 100K output tokens
		CacheReadTokens:  500000,  // 500K cache read
		CacheWriteTokens: 200000,  // 200K cache write
	}

	cost := analytics.CalculateCost("claude-3-5-sonnet")

	// Same pricing as Sonnet 4: $5.40
	assert.InDelta(t, 5.40, cost, 0.01)
}

// ============================================================================
// Billing Block Tests
// ============================================================================

func TestCalculateBillingBlocks_Basic(t *testing.T) {
	// Create entries spanning 7 hours (should be 2 blocks with 5-hour windows)
	now := time.Now()
	entries := []time.Time{
		now.Add(-6 * time.Hour), // Block 1 start
		now.Add(-4 * time.Hour), // Block 1 (within 5h of first)
		now.Add(-2 * time.Hour), // Block 2 start (6h - 2h = 4h gap, but -2h is >5h from -6h)
		now,                     // Block 2 (current)
	}

	blocks := CalculateBillingBlocks(entries, 5*time.Hour)

	assert.Equal(t, 2, len(blocks))
	assert.True(t, blocks[1].IsActive) // Current block is active
}

func TestCalculateBillingBlocks_EmptyInput(t *testing.T) {
	blocks := CalculateBillingBlocks([]time.Time{}, 5*time.Hour)

	assert.Nil(t, blocks)
	assert.Equal(t, 0, len(blocks))
}

func TestCalculateBillingBlocks_SingleEntry(t *testing.T) {
	now := time.Now()
	entries := []time.Time{now}

	blocks := CalculateBillingBlocks(entries, 5*time.Hour)

	assert.Equal(t, 1, len(blocks))
	assert.Equal(t, now, blocks[0].StartTime)
	assert.Equal(t, now, blocks[0].EndTime)
	assert.True(t, blocks[0].IsActive)
}

func TestCalculateBillingBlocks_AllInOneBlock(t *testing.T) {
	now := time.Now()
	entries := []time.Time{
		now.Add(-4 * time.Hour),
		now.Add(-3 * time.Hour),
		now.Add(-2 * time.Hour),
		now.Add(-1 * time.Hour),
		now,
	}

	blocks := CalculateBillingBlocks(entries, 5*time.Hour)

	assert.Equal(t, 1, len(blocks))
	assert.True(t, blocks[0].IsActive)
}

func TestCalculateBillingBlocks_UnsortedInput(t *testing.T) {
	now := time.Now()
	// Input is NOT sorted - function should handle this
	entries := []time.Time{
		now.Add(-2 * time.Hour),
		now.Add(-6 * time.Hour),
		now,
		now.Add(-4 * time.Hour),
	}

	blocks := CalculateBillingBlocks(entries, 5*time.Hour)

	// After sorting: -6h, -4h, -2h, now
	// Block 1: -6h to -4h (within 5h window)
	// Block 2: -2h to now (gap from -4h to -2h is 2h, but -2h is 4h from -6h, still within 5h)
	// Actually: -6h, -4h are within 5h of -6h
	//           -2h is 4h from -6h, still within 5h
	//           now is 6h from -6h, so new block
	// So should be 2 blocks
	assert.Equal(t, 2, len(blocks))
}

func TestCalculateBillingBlocks_OldSession(t *testing.T) {
	// Session from yesterday - no active block
	yesterday := time.Now().Add(-24 * time.Hour)
	entries := []time.Time{
		yesterday.Add(-2 * time.Hour),
		yesterday.Add(-1 * time.Hour),
		yesterday,
	}

	blocks := CalculateBillingBlocks(entries, 5*time.Hour)

	assert.Equal(t, 1, len(blocks))
	assert.False(t, blocks[0].IsActive) // Old session, not active
}

func TestCalculateBillingBlocks_MultipleBlocks(t *testing.T) {
	now := time.Now()
	entries := []time.Time{
		now.Add(-15 * time.Hour), // Block 1
		now.Add(-14 * time.Hour), // Block 1
		now.Add(-8 * time.Hour),  // Block 2 (gap > 5h from block 1)
		now.Add(-7 * time.Hour),  // Block 2
		now.Add(-1 * time.Hour),  // Block 3 (gap > 5h from block 2)
		now,                      // Block 3
	}

	blocks := CalculateBillingBlocks(entries, 5*time.Hour)

	assert.Equal(t, 3, len(blocks))
	assert.False(t, blocks[0].IsActive)
	assert.False(t, blocks[1].IsActive)
	assert.True(t, blocks[2].IsActive)
}

func TestCalculateBillingBlocks_ExactlyAtWindowBoundary(t *testing.T) {
	now := time.Now()
	entries := []time.Time{
		now.Add(-5 * time.Hour), // Exactly 5h from start
		now,                     // Should start new block
	}

	blocks := CalculateBillingBlocks(entries, 5*time.Hour)

	// Entry at exactly 5h should start a new block (>= windowSize)
	assert.Equal(t, 2, len(blocks))
}

func TestCalculateBillingBlocks_JustUnderWindowBoundary(t *testing.T) {
	now := time.Now()
	entries := []time.Time{
		now.Add(-4*time.Hour - 59*time.Minute), // Just under 5h
		now,                                    // Should be in same block
	}

	blocks := CalculateBillingBlocks(entries, 5*time.Hour)

	// Entry just under 5h should be in same block
	assert.Equal(t, 1, len(blocks))
	assert.True(t, blocks[0].IsActive)
}

func TestCalculateBillingBlocks_CustomWindowSize(t *testing.T) {
	now := time.Now()
	entries := []time.Time{
		now.Add(-3 * time.Hour),
		now.Add(-1 * time.Hour),
		now,
	}

	// Use 2-hour window instead of 5-hour
	blocks := CalculateBillingBlocks(entries, 2*time.Hour)

	// -3h starts block 1
	// -1h is 2h from -3h, so starts block 2
	// now is 1h from -1h, so same block 2
	assert.Equal(t, 2, len(blocks))
}
