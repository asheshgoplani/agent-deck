package costs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseClaudeHookPayload(t *testing.T) {
	pricer := NewPricer(PricerConfig{})
	parser := &ClaudeHookParser{pricer: pricer}

	input := `{"hook_event_name":"Stop","session_id":"abc","source":"claude","result":{"usage":{"input_tokens":4231,"output_tokens":1892,"cache_creation_input_tokens":500,"cache_read_input_tokens":3500},"model":"claude-sonnet-4-6-20260301"}}`

	events, err := parser.Parse(input)
	require.NoError(t, err)
	require.Len(t, events, 1)

	ev := events[0]
	assert.Equal(t, "claude-sonnet-4-6-20260301", ev.Model)
	assert.Equal(t, int64(4231), ev.InputTokens)
	assert.Equal(t, int64(1892), ev.OutputTokens)
	assert.Equal(t, int64(3500), ev.CacheReadTokens)
	assert.Equal(t, int64(500), ev.CacheWriteTokens)
}

func TestParseClaudeHookPayload_NoUsage(t *testing.T) {
	pricer := NewPricer(PricerConfig{})
	parser := &ClaudeHookParser{pricer: pricer}

	// Non-Stop event
	input := `{"hook_event_name":"Start","session_id":"abc","source":"claude","result":{}}`

	events, err := parser.Parse(input)
	require.NoError(t, err)
	assert.Empty(t, events)
}

func TestGeminiParser(t *testing.T) {
	pricer := NewPricer(PricerConfig{})
	parser := &GeminiOutputParser{pricer: pricer}

	events, err := parser.Parse("Token count: 1,234 input, 567 output")
	require.NoError(t, err)
	require.Len(t, events, 1)

	assert.Equal(t, int64(1234), events[0].InputTokens)
	assert.Equal(t, int64(567), events[0].OutputTokens)
	assert.Equal(t, "gemini-2.5-pro", events[0].Model)
}

func TestGeminiParser_NoMatch(t *testing.T) {
	pricer := NewPricer(PricerConfig{})
	parser := &GeminiOutputParser{pricer: pricer}

	events, err := parser.Parse("no usage info here")
	require.NoError(t, err)
	assert.Empty(t, events)
}

func TestOpenAIParser(t *testing.T) {
	pricer := NewPricer(PricerConfig{})
	parser := &OpenAIOutputParser{pricer: pricer}

	events, err := parser.Parse("Tokens used: 2,450 prompt + 890 completion (gpt-4.1)")
	require.NoError(t, err)
	require.Len(t, events, 1)

	assert.Equal(t, int64(2450), events[0].InputTokens)
	assert.Equal(t, int64(890), events[0].OutputTokens)
	assert.Equal(t, "gpt-4.1", events[0].Model)
}

func TestCollector(t *testing.T) {
	pricer := NewPricer(PricerConfig{})
	collector := NewCollector(pricer)

	input := `{"hook_event_name":"Stop","session_id":"test","source":"claude","result":{"usage":{"input_tokens":1000,"output_tokens":500},"model":"claude-sonnet-4-6"}}`

	events, err := collector.Collect("claude", "session-123", input)
	require.NoError(t, err)
	require.Len(t, events, 1)

	ev := events[0]
	assert.Equal(t, "session-123", ev.SessionID)
	assert.Equal(t, "claude-sonnet-4-6", ev.Model)
	assert.NotEmpty(t, ev.ID)
	assert.Greater(t, ev.CostMicrodollars, int64(0))
}
