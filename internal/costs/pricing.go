package costs

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// ModelPrice holds per-model pricing in microdollars per million tokens.
type ModelPrice struct {
	InputPerMtokMicro      int64
	OutputPerMtokMicro     int64
	CacheReadPerMtokMicro  int64
	CacheWritePerMtokMicro int64
}

// PriceOverride holds user-configured pricing in USD per million tokens.
type PriceOverride struct {
	InputPerMtok      float64 `toml:"input_per_mtok"`
	OutputPerMtok     float64 `toml:"output_per_mtok"`
	CacheReadPerMtok  float64 `toml:"cache_read_per_mtok"`
	CacheWritePerMtok float64 `toml:"cache_write_per_mtok"`
}

// PricerConfig configures the Pricer.
type PricerConfig struct {
	CachePath string
	Overrides map[string]PriceOverride
}

// Pricer resolves model pricing with fallback: override > cache > hardcoded.
type Pricer struct {
	defaults  map[string]ModelPrice
	cached    map[string]ModelPrice
	overrides map[string]ModelPrice
	cachePath string
	cacheTime time.Time
}

type pricingCacheFile struct {
	FetchedAt time.Time                    `json:"fetched_at"`
	Models    map[string]pricingCacheModel `json:"models"`
}

type pricingCacheModel struct {
	InputPerMtok      float64 `json:"input_per_mtok"`
	OutputPerMtok     float64 `json:"output_per_mtok"`
	CacheReadPerMtok  float64 `json:"cache_read_per_mtok"`
	CacheWritePerMtok float64 `json:"cache_write_per_mtok"`
}

func usdToMicro(usd float64) int64 {
	return int64(math.Round(usd * 1_000_000))
}

func priceFromUSD(input, output, cacheRead, cacheWrite float64) ModelPrice {
	return ModelPrice{
		InputPerMtokMicro:      usdToMicro(input),
		OutputPerMtokMicro:     usdToMicro(output),
		CacheReadPerMtokMicro:  usdToMicro(cacheRead),
		CacheWritePerMtokMicro: usdToMicro(cacheWrite),
	}
}

// NewPricer creates a Pricer with hardcoded defaults and optional overrides.
func NewPricer(cfg PricerConfig) *Pricer {
	p := &Pricer{
		defaults: map[string]ModelPrice{
			// Anthropic rates: https://docs.anthropic.com/en/docs/about-claude/pricing
			"claude-opus-4-7":   priceFromUSD(5.0, 25.0, 0.50, 6.25),
			"claude-opus-4-6":   priceFromUSD(5.0, 25.0, 0.50, 6.25),
			"claude-sonnet-4-6": priceFromUSD(3.0, 15.0, 0.30, 3.75),
			"claude-haiku-4-5":  priceFromUSD(1.0, 5.0, 0.10, 1.25),
			"gemini-2.5-pro":    priceFromUSD(1.25, 10.0, 0, 0),
			"gemini-2.5-flash":  priceFromUSD(0.15, 0.60, 0, 0),
			"gpt-4o":            priceFromUSD(2.50, 10.0, 0, 0),
			"gpt-4.1":           priceFromUSD(2.0, 8.0, 0, 0),
			"o3":                priceFromUSD(2.0, 8.0, 0, 0),
			"o4-mini":           priceFromUSD(1.10, 4.40, 0, 0),
			// MiniMax models (https://www.minimaxi.com/en/pricing)
			"MiniMax-M2.7":           priceFromUSD(0.70, 2.80, 0, 0),
			"MiniMax-M2.7-highspeed": priceFromUSD(0.35, 1.40, 0, 0),
			"MiniMax-M2.5":           priceFromUSD(0.50, 2.00, 0, 0),
			"MiniMax-M2.5-highspeed": priceFromUSD(0.15, 0.60, 0, 0),
		},
		cached:    make(map[string]ModelPrice),
		overrides: make(map[string]ModelPrice),
		cachePath: cfg.CachePath,
	}

	for model, ov := range cfg.Overrides {
		p.overrides[model] = ModelPrice{
			InputPerMtokMicro:      usdToMicro(ov.InputPerMtok),
			OutputPerMtokMicro:     usdToMicro(ov.OutputPerMtok),
			CacheReadPerMtokMicro:  usdToMicro(ov.CacheReadPerMtok),
			CacheWritePerMtokMicro: usdToMicro(ov.CacheWritePerMtok),
		}
	}

	return p
}

// LoadCache reads pricing.json from CachePath.
func (p *Pricer) LoadCache() error {
	if p.cachePath == "" {
		return nil
	}
	path := filepath.Join(p.cachePath, "pricing.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var cf pricingCacheFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return err
	}
	p.cacheTime = cf.FetchedAt
	p.cached = make(map[string]ModelPrice, len(cf.Models))
	for model, m := range cf.Models {
		p.cached[model] = ModelPrice{
			InputPerMtokMicro:      usdToMicro(m.InputPerMtok),
			OutputPerMtokMicro:     usdToMicro(m.OutputPerMtok),
			CacheReadPerMtokMicro:  usdToMicro(m.CacheReadPerMtok),
			CacheWritePerMtokMicro: usdToMicro(m.CacheWritePerMtok),
		}
	}
	return nil
}

// SaveCache writes pricing.json to CachePath.
func (p *Pricer) SaveCache(models map[string]pricingCacheModel) error {
	if p.cachePath == "" {
		return nil
	}
	cf := pricingCacheFile{
		FetchedAt: time.Now().UTC(),
		Models:    models,
	}
	data, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.cachePath, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(p.cachePath, "pricing.json"), data, 0o644)
}

// CacheAge returns the age of the cache, or -1 if no cache is loaded.
func (p *Pricer) CacheAge() time.Duration {
	if p.cacheTime.IsZero() {
		return -1
	}
	return time.Since(p.cacheTime)
}

// GetPrice returns the price for a model with fallback: override > cache > hardcoded.
func (p *Pricer) GetPrice(model string) (ModelPrice, bool) {
	normalized := normalizeModel(model)
	if mp, ok := p.overrides[normalized]; ok {
		return mp, true
	}
	if mp, ok := p.cached[normalized]; ok {
		return mp, true
	}
	if mp, ok := p.defaults[normalized]; ok {
		return mp, true
	}
	return ModelPrice{}, false
}

// ComputeCost calculates cost in microdollars for token usage on a model.
func (p *Pricer) ComputeCost(model string, input, output, cacheRead, cacheWrite int64) int64 {
	mp, ok := p.GetPrice(model)
	if !ok {
		return 0
	}
	cost := float64(input)*float64(mp.InputPerMtokMicro)/1_000_000 +
		float64(output)*float64(mp.OutputPerMtokMicro)/1_000_000 +
		float64(cacheRead)*float64(mp.CacheReadPerMtokMicro)/1_000_000 +
		float64(cacheWrite)*float64(mp.CacheWritePerMtokMicro)/1_000_000
	return int64(math.Round(cost))
}

var dateSuffixRe = regexp.MustCompile(`-\d{8}$`)

// normalizeModel strips date suffixes from model names.
func normalizeModel(model string) string {
	return dateSuffixRe.ReplaceAllString(model, "")
}
