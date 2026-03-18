package costs_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/costs"
)

func TestFetcherCacheAge_Missing(t *testing.T) {
	f := &costs.Fetcher{CachePath: filepath.Join(t.TempDir(), "missing.json")}
	if f.CacheAge() >= 0 {
		t.Error("missing file should have negative age")
	}
}

func TestFetcherCacheAge_Exists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pricing.json")
	if err := os.WriteFile(path, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}

	f := &costs.Fetcher{CachePath: path}
	if f.CacheAge() < 0 {
		t.Error("existing file should have non-negative age")
	}
}

func TestFetcherWritesCache(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "pricing.json")

	pricer := costs.NewPricer(costs.PricerConfig{CachePath: dir})
	f := &costs.Fetcher{CachePath: cachePath, Pricer: pricer}

	if err := f.FetchAndCache(); err != nil {
		t.Fatal(err)
	}

	// Verify file was written
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("cache file not created: %v", err)
	}

	// Load and verify a model is present
	if err := pricer.LoadCache(); err != nil {
		t.Fatal(err)
	}
	price, ok := pricer.GetPrice("claude-sonnet-4-6")
	if !ok {
		t.Fatal("missing price after cache load")
	}
	if price.InputPerMtokMicro != 3_000_000 {
		t.Errorf("input = %d, want 3000000", price.InputPerMtokMicro)
	}
}
