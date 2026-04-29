package costs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHardcodedPricing(t *testing.T) {
	p := NewPricer(PricerConfig{})
	mp, ok := p.GetPrice("claude-sonnet-4-6")
	assert.True(t, ok)
	assert.Greater(t, mp.InputPerMtokMicro, int64(0))
	assert.Greater(t, mp.OutputPerMtokMicro, int64(0))
	assert.Greater(t, mp.CacheReadPerMtokMicro, int64(0))
	assert.Greater(t, mp.CacheWritePerMtokMicro, int64(0))
}

func TestPricerComputeCost(t *testing.T) {
	p := NewPricer(PricerConfig{})
	// claude-sonnet-4-6: input=$3/Mtok, output=$15/Mtok
	// 1M input = $3, 1M output = $15 → total $18 = 18_000_000 microdollars
	cost := p.ComputeCost("claude-sonnet-4-6", 1_000_000, 1_000_000, 0, 0)
	assert.Equal(t, int64(18_000_000), cost)
}

func TestPricerCacheFile(t *testing.T) {
	dir := t.TempDir()
	p := NewPricer(PricerConfig{CachePath: dir})

	// Save custom pricing
	err := p.SaveCache(map[string]pricingCacheModel{
		"custom-model": {
			InputPerMtok:  5.0,
			OutputPerMtok: 20.0,
		},
	})
	require.NoError(t, err)

	// Verify file exists
	_, err = os.Stat(filepath.Join(dir, "pricing.json"))
	require.NoError(t, err)

	// Load and verify
	p2 := NewPricer(PricerConfig{CachePath: dir})
	err = p2.LoadCache()
	require.NoError(t, err)

	mp, ok := p2.GetPrice("custom-model")
	assert.True(t, ok)
	assert.Equal(t, int64(5_000_000), mp.InputPerMtokMicro)
	assert.Equal(t, int64(20_000_000), mp.OutputPerMtokMicro)
}

func TestPricerUserOverride(t *testing.T) {
	p := NewPricer(PricerConfig{
		Overrides: map[string]PriceOverride{
			"claude-sonnet-4-6": {
				InputPerMtok:  99.0,
				OutputPerMtok: 99.0,
			},
		},
	})
	mp, ok := p.GetPrice("claude-sonnet-4-6")
	assert.True(t, ok)
	// Override should take precedence over hardcoded
	assert.Equal(t, int64(99_000_000), mp.InputPerMtokMicro)
	assert.Equal(t, int64(99_000_000), mp.OutputPerMtokMicro)
}

func TestMiniMaxPricing(t *testing.T) {
	p := NewPricer(PricerConfig{})

	tests := []struct {
		model  string
		input  int64
		output int64
	}{
		{"MiniMax-M2.7", 700_000, 2_800_000},
		{"MiniMax-M2.7-highspeed", 350_000, 1_400_000},
		{"MiniMax-M2.5", 500_000, 2_000_000},
		{"MiniMax-M2.5-highspeed", 150_000, 600_000},
	}

	for _, tt := range tests {
		mp, ok := p.GetPrice(tt.model)
		assert.True(t, ok, "model %s should have pricing", tt.model)
		assert.Equal(t, tt.input, mp.InputPerMtokMicro, "model %s input pricing", tt.model)
		assert.Equal(t, tt.output, mp.OutputPerMtokMicro, "model %s output pricing", tt.model)
	}
}

func TestMiniMaxComputeCost(t *testing.T) {
	p := NewPricer(PricerConfig{})
	// MiniMax-M2.7: input=$0.70/Mtok, output=$2.80/Mtok
	// 1M input = $0.70, 1M output = $2.80 → total $3.50 = 3,500,000 microdollars
	cost := p.ComputeCost("MiniMax-M2.7", 1_000_000, 1_000_000, 0, 0)
	assert.Equal(t, int64(3_500_000), cost)
}

func TestPricerModelNormalization(t *testing.T) {
	p := NewPricer(PricerConfig{})
	mp, ok := p.GetPrice("claude-sonnet-4-6-20260301")
	assert.True(t, ok)
	assert.Equal(t, int64(3_000_000), mp.InputPerMtokMicro)
}

// TestAnthropicPricing pins exact rates for every Anthropic model in defaults
// against Anthropic's published rates. Source:
// https://docs.anthropic.com/en/docs/about-claude/pricing
// Cache-write column is the 5-minute TTL rate (the cache write rate the
// pricing.json schema represents).
func TestAnthropicPricing(t *testing.T) {
	p := NewPricer(PricerConfig{})

	tests := []struct {
		model      string
		input      int64
		output     int64
		cacheRead  int64
		cacheWrite int64
	}{
		{"claude-opus-4-7", 5_000_000, 25_000_000, 500_000, 6_250_000},
		{"claude-opus-4-6", 5_000_000, 25_000_000, 500_000, 6_250_000},
		{"claude-sonnet-4-6", 3_000_000, 15_000_000, 300_000, 3_750_000},
		{"claude-haiku-4-5", 1_000_000, 5_000_000, 100_000, 1_250_000},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			mp, ok := p.GetPrice(tt.model)
			require.True(t, ok, "model %s should have pricing", tt.model)
			assert.Equal(t, tt.input, mp.InputPerMtokMicro, "input")
			assert.Equal(t, tt.output, mp.OutputPerMtokMicro, "output")
			assert.Equal(t, tt.cacheRead, mp.CacheReadPerMtokMicro, "cache_read")
			assert.Equal(t, tt.cacheWrite, mp.CacheWritePerMtokMicro, "cache_write")
		})
	}
}
