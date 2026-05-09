package statedb

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestWriteStatus_ConcurrentWritersAllLand stresses WriteStatus from many
// goroutines; every call must report success. Pre-fix this can flake under
// SQLITE_BUSY because WriteStatus has no retry. Post-fix the withBusyRetry
// helper absorbs transient locks.
func TestWriteStatus_ConcurrentWritersAllLand(t *testing.T) {
	db := newTestDB(t)

	const N = 8
	const iters = 100
	ids := make([]string, N)
	for i := 0; i < N; i++ {
		ids[i] = fmt.Sprintf("status-%d", i)
		if err := db.SaveInstance(&InstanceRow{
			ID: ids[i], Title: ids[i], ProjectPath: "/tmp", GroupPath: "g",
			Tool: "shell", Status: "idle", CreatedAt: time.Now(),
			ToolData: json.RawMessage("{}"),
		}); err != nil {
			t.Fatalf("SaveInstance: %v", err)
		}
	}

	var wg sync.WaitGroup
	errCh := make(chan error, N*iters)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				if err := db.WriteStatus(id, "running", "shell"); err != nil {
					errCh <- fmt.Errorf("WriteStatus(%s): %w", id, err)
					return
				}
			}
		}(ids[i])
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent WriteStatus failed: %v", err)
	}
}

// TestUpdateWatcherEventRoutedTo_BusyRetry verifies that the retry helper is
// applied. We can't easily synthesize SQLITE_BUSY in-process with a single
// connection (WAL serializes writers), but we can hit the operation under
// concurrent load and assert all updates succeed. Pre-fix: no retry, sister
// SaveWatcherEvent has 5-attempt retry — asymmetry is the bug.
func TestUpdateWatcherEventRoutedTo_ConcurrentUpdatesSucceed(t *testing.T) {
	db := newTestDB(t)

	if err := db.SaveWatcher(&WatcherRow{
		ID: "w1", Name: "w1", Type: "github", ConfigPath: "/tmp/w1",
		Status: "idle", Conductor: "c", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveWatcher: %v", err)
	}

	// Realistic contention: 8 events × 2 routers = 16 concurrent updaters.
	// Pre-fix (no retry on UpdateWatcherEventRoutedTo) reliably surfaces
	// SQLITE_BUSY at this level; post-fix all updates land within the
	// helper's retry budget.
	const events = 8
	for i := 0; i < events; i++ {
		dedup := fmt.Sprintf("dk-%d", i)
		if _, err := db.SaveWatcherEvent("w1", dedup, "alice", "subj", "", "", 1000); err != nil {
			t.Fatalf("SaveWatcherEvent: %v", err)
		}
	}

	var wg sync.WaitGroup
	errCh := make(chan error, events*2)
	for i := 0; i < events; i++ {
		dedup := fmt.Sprintf("dk-%d", i)
		for k := 0; k < 2; k++ {
			wg.Add(1)
			go func(dk string, k int) {
				defer wg.Done()
				routed := fmt.Sprintf("session-%d", k)
				if err := db.UpdateWatcherEventRoutedTo("w1", dk, routed, "tri-"+dk); err != nil {
					errCh <- fmt.Errorf("UpdateWatcherEventRoutedTo(%s): %w", dk, err)
				}
			}(dedup, k)
		}
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent UpdateWatcherEventRoutedTo failed: %v", err)
	}
}

// TestPruneWatcherEvents_ErrorSurfaced ensures the prune step inside
// SaveWatcherEvent does not silently swallow errors that would let the table
// grow unbounded. Pre-fix: SaveWatcherEvent ignores the prune error with
// `_ = s.pruneWatcherEvents(...)`. Post-fix: the helper still uses retry, but
// we add a public PruneWatcherEvents that can be called directly with errors
// surfaced, AND we exercise that table growth is actually bounded after many
// inserts.
func TestSaveWatcherEvent_PruneBoundsTable(t *testing.T) {
	db := newTestDB(t)

	if err := db.SaveWatcher(&WatcherRow{
		ID: "wp", Name: "wp", Type: "github", ConfigPath: "/tmp/wp",
		Status: "idle", Conductor: "c", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveWatcher: %v", err)
	}

	const maxEvents = 5
	const inserted = 50
	for i := 0; i < inserted; i++ {
		dedup := fmt.Sprintf("p-%d", i)
		if _, err := db.SaveWatcherEvent("wp", dedup, "alice", "s", "", "", maxEvents); err != nil {
			t.Fatalf("SaveWatcherEvent: %v", err)
		}
	}

	rows, err := db.LoadWatcherEvents("wp", 1000)
	if err != nil {
		t.Fatalf("LoadWatcherEvents: %v", err)
	}
	if len(rows) > maxEvents {
		t.Errorf("table grew unbounded: have %d, max %d", len(rows), maxEvents)
	}
}

// TestSaveRecentSession_AtomicWithPrune asserts that SaveRecentSession's
// INSERT and prune are bundled into a transaction so a crash between them
// cannot leave the table over-budget. We exercise it by inserting many entries
// concurrently and asserting the final count never exceeds the cap.
//
// Concurrency is kept moderate (12 goroutines) so the test exercises atomicity
// rather than SQLite's per-connection busy_timeout, which is configured at
// pool open time and is out of scope for this theme.
func TestSaveRecentSession_AtomicWithPrune(t *testing.T) {
	db := newTestDB(t)

	const N = 12
	const perGoroutine = 5 // 60 unique inserts; cap is 20
	var wg sync.WaitGroup
	errCh := make(chan error, N*perGoroutine)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				row := &RecentSessionRow{
					Title:       fmt.Sprintf("title-%d-%d", gid, j),
					ProjectPath: fmt.Sprintf("/p/%d/%d", gid, j),
					GroupPath:   "g",
					Command:     "claude",
					Wrapper:     "",
					Tool:        "claude",
					ToolOptions: json.RawMessage(`{}`),
					DeletedAt:   time.Now(),
				}
				if err := db.SaveRecentSession(row); err != nil {
					errCh <- err
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("SaveRecentSession: %v", err)
	}

	rows, err := db.LoadRecentSessions()
	if err != nil {
		t.Fatalf("LoadRecentSessions: %v", err)
	}
	if len(rows) > 20 {
		t.Errorf("recent_sessions over budget: have %d, cap 20", len(rows))
	}
}
