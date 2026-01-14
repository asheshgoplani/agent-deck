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
		InputTokens:      1000,
		OutputTokens:     500,
		CacheReadTokens:  200,
		CacheWriteTokens: 100,
	}

	// 1800 tokens / 200000 limit * 100 = 0.9%
	assert.InDelta(t, 0.9, analytics.ContextPercent(200000), 0.01)
}

func TestSessionAnalytics_ContextPercent_DefaultLimit(t *testing.T) {
	analytics := &SessionAnalytics{
		InputTokens:  20000,
		OutputTokens: 0,
	}

	// 20000 tokens / 200000 default limit * 100 = 10%
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
