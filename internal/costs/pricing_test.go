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

func TestPricerModelNormalization(t *testing.T) {
	p := NewPricer(PricerConfig{})
	mp, ok := p.GetPrice("claude-sonnet-4-6-20260301")
	assert.True(t, ok)
	assert.Equal(t, int64(3_000_000), mp.InputPerMtokMicro)
}
