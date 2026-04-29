package costs

import (
	"context"
	"log/slog"
	"os"
	"time"
)

type Fetcher struct {
	CachePath string
	Pricer    *Pricer
	Logger    *slog.Logger
}

func (f *Fetcher) CacheAge() time.Duration {
	info, err := os.Stat(f.CachePath)
	if err != nil {
		return -1
	}
	return time.Since(info.ModTime())
}

// FetchAndCache writes current known prices to cache.
// Real HTML scraping is deferred — for now, writes hardcoded defaults.
func (f *Fetcher) FetchAndCache() error {
	defaults := map[string]pricingCacheModel{
		// Anthropic rates: https://docs.anthropic.com/en/docs/about-claude/pricing
		"claude-opus-4-7":   {InputPerMtok: 5.0, OutputPerMtok: 25.0, CacheReadPerMtok: 0.50, CacheWritePerMtok: 6.25},
		"claude-opus-4-6":   {InputPerMtok: 5.0, OutputPerMtok: 25.0, CacheReadPerMtok: 0.50, CacheWritePerMtok: 6.25},
		"claude-sonnet-4-6": {InputPerMtok: 3.0, OutputPerMtok: 15.0, CacheReadPerMtok: 0.30, CacheWritePerMtok: 3.75},
		"claude-haiku-4-5":  {InputPerMtok: 1.0, OutputPerMtok: 5.0, CacheReadPerMtok: 0.10, CacheWritePerMtok: 1.25},
		"gemini-2.5-pro":    {InputPerMtok: 1.25, OutputPerMtok: 10.0},
		"gemini-2.5-flash":  {InputPerMtok: 0.15, OutputPerMtok: 0.60},
		"gpt-4o":            {InputPerMtok: 2.50, OutputPerMtok: 10.0},
		"gpt-4.1":           {InputPerMtok: 2.0, OutputPerMtok: 8.0},
		"o3":                {InputPerMtok: 2.0, OutputPerMtok: 8.0},
		"o4-mini":           {InputPerMtok: 1.10, OutputPerMtok: 4.40},
	}

	if f.Pricer != nil {
		return f.Pricer.SaveCache(defaults)
	}
	return nil
}

// StartDaily runs the fetch loop. Blocks until context is cancelled.
func (f *Fetcher) StartDaily(ctx context.Context) {
	if f.CacheAge() > 24*time.Hour || f.CacheAge() < 0 {
		if err := f.FetchAndCache(); err != nil && f.Logger != nil {
			f.Logger.Warn("pricing_fetch_failed", slog.String("error", err.Error()))
		}
	}

	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := f.FetchAndCache(); err != nil && f.Logger != nil {
				f.Logger.Warn("pricing_fetch_failed", slog.String("error", err.Error()))
			}
		}
	}
}
