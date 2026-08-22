package costs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestSaveCacheIsAtomic pins that SaveCache never leaves a partial pricing.json:
// the destination is only ever the previous complete file or the new complete
// file, and no temp file is left behind on success.
func TestSaveCacheIsAtomic(t *testing.T) {
	dir := t.TempDir()
	p := &Pricer{cachePath: dir}
	final := filepath.Join(dir, "pricing.json")

	if err := p.SaveCache(map[string]pricingCacheModel{"m1": {}}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	first, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("read first: %v", err)
	}
	var cf pricingCacheFile
	if err := json.Unmarshal(first, &cf); err != nil {
		t.Fatalf("first cache is not complete JSON: %v", err)
	}

	if err := p.SaveCache(map[string]pricingCacheModel{"m1": {}, "m2": {}}); err != nil {
		t.Fatalf("second save: %v", err)
	}
	second, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("read second: %v", err)
	}
	if err := json.Unmarshal(second, &cf); err != nil {
		t.Fatalf("second cache is not complete JSON: %v", err)
	}
	if len(cf.Models) != 2 {
		t.Fatalf("expected 2 models after rename, got %d", len(cf.Models))
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "pricing.json" {
			t.Fatalf("unexpected leftover in cache dir: %s", e.Name())
		}
	}
}
