//go:build hostsensitive

// Host-sensitive statedb tests. Built and run only when the `hostsensitive`
// build tag is supplied (e.g. nightly job: `go test -tags hostsensitive`).
// See issue #969 for the categorization rationale.

package statedb

import (
	"sync"
	"testing"
	"time"
)

// TestWatcherEventDedup races two goroutines on a shared SQLite handle. Under
// `-race` on some hosts (kernel scheduling + busy-handler timing) this trips
// SQLITE_BUSY and fails non-deterministically. The dedup contract is covered
// by serial unit tests; this concurrent variant runs only under
// `-tags hostsensitive` so the default pre-push / CI path stays deterministic.
func TestWatcherEventDedup(t *testing.T) {
	db := newTestDB(t)

	// Insert parent watcher row (required by FK constraint)
	if err := db.SaveWatcher(&WatcherRow{
		ID: "w1", Name: "dedup-test", Type: "webhook",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveWatcher: %v", err)
	}

	// Two goroutines racing to insert the same dedup key
	var wg sync.WaitGroup
	results := make([]bool, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			inserted, err := db.SaveWatcherEvent("w1", "same-dedup-key", "sender@test.com", "subject", "conductor-a", "", 500)
			if err != nil {
				t.Errorf("goroutine %d: SaveWatcherEvent error: %v", idx, err)
				return
			}
			results[idx] = inserted
		}(i)
	}
	wg.Wait()

	var count int
	if err := db.DB().QueryRow("SELECT COUNT(*) FROM watcher_events WHERE watcher_id='w1'").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 row after concurrent dedup inserts, got %d", count)
	}

	// Exactly one goroutine should have reported inserted=true
	insertCount := 0
	for _, r := range results {
		if r {
			insertCount++
		}
	}
	if insertCount != 1 {
		t.Errorf("expected exactly 1 goroutine to report inserted=true, got %d", insertCount)
	}
}
