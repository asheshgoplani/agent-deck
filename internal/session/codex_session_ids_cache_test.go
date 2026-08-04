package session

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCodexSessionIDsCacheCoalescesConcurrentReads(t *testing.T) {
	var calls atomic.Int32
	cache := newCodexSessionIDsCache(time.Second)
	collect := func() (map[string]string, error) {
		calls.Add(1)
		return map[string]string{"agentdeck_one": "one"}, nil
	}

	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			got, err := cache.load(collect)
			if err != nil {
				t.Errorf("cache.load: %v", err)
			}
			if got["agentdeck_one"] != "one" {
				t.Errorf("cache result = %#v", got)
			}
		}()
	}
	workers.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("collector calls = %d, want 1", got)
	}
}
