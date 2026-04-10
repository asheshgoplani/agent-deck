package costs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Integration tests verify the full cost pipeline for MiniMax models:
// output parsing → token extraction → pricing lookup → cost computation.

func TestMiniMaxEndToEnd_M27(t *testing.T) {
	pricer := NewPricer(PricerConfig{})
	collector := NewCollector(pricer)

	input := "MiniMax usage: 500,000 input tokens, 100,000 output tokens (MiniMax-M2.7)"
	events, err := collector.Collect("minimax", "integration-m27", input)
	require.NoError(t, err)
	require.Len(t, events, 1)

	ev := events[0]
	assert.Equal(t, "integration-m27", ev.SessionID)
	assert.Equal(t, "MiniMax-M2.7", ev.Model)
	assert.Equal(t, int64(500_000), ev.InputTokens)
	assert.Equal(t, int64(100_000), ev.OutputTokens)
	// 500K input at $0.70/Mtok = $0.35 = 350,000 microdollars
	// 100K output at $2.80/Mtok = $0.28 = 280,000 microdollars
	// Total = 630,000 microdollars
	assert.Equal(t, int64(630_000), ev.CostMicrodollars)
}

func TestMiniMaxEndToEnd_M25Highspeed(t *testing.T) {
	pricer := NewPricer(PricerConfig{})
	collector := NewCollector(pricer)

	input := "MiniMax usage: 1,000,000 input tokens, 500,000 output tokens (MiniMax-M2.5-highspeed)"
	events, err := collector.Collect("minimax", "integration-m25hs", input)
	require.NoError(t, err)
	require.Len(t, events, 1)

	ev := events[0]
	assert.Equal(t, "MiniMax-M2.5-highspeed", ev.Model)
	// 1M input at $0.15/Mtok = $0.15 = 150,000 microdollars
	// 500K output at $0.60/Mtok = $0.30 = 300,000 microdollars
	// Total = 450,000 microdollars
	assert.Equal(t, int64(450_000), ev.CostMicrodollars)
}

func TestMiniMaxEndToEnd_UnknownTool(t *testing.T) {
	pricer := NewPricer(PricerConfig{})
	collector := NewCollector(pricer)

	// MiniMax output sent via an unknown tool type should not be parsed by any parser
	input := "MiniMax usage: 1,000 input tokens, 500 output tokens (MiniMax-M2.7)"
	events, err := collector.Collect("unknown-tool", "session-wrong-tool", input)
	require.NoError(t, err)
	assert.Nil(t, events)
}
