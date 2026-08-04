package session

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProcessTableSnapshotCacheCoalescesConcurrentReads(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	collect := func() ([]byte, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return []byte("101 1\n102 101\n"), nil
	}
	oldCache := codexProcessTableCache
	oldCollector := codexProcessTableCollector
	codexProcessTableCache = newProcessTableSnapshotCache(time.Second)
	codexProcessTableCollector = collect
	t.Cleanup(func() {
		codexProcessTableCache = oldCache
		codexProcessTableCollector = oldCollector
	})

	const readers = 8
	results := make(chan []byte, readers)
	var workers sync.WaitGroup
	for range readers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			got, err := loadCodexProcessTable()
			if err != nil {
				t.Errorf("cache.load: %v", err)
				return
			}
			results <- got
		}()
	}

	<-started
	close(release)
	workers.Wait()
	close(results)

	if got := calls.Load(); got != 1 {
		t.Fatalf("process-table collector calls = %d, want 1", got)
	}
	for got := range results {
		if string(got) != "101 1\n102 101\n" {
			t.Errorf("cache result = %q", got)
		}
	}
}
