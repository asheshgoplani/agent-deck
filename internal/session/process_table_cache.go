package session

import (
	"os/exec"
	"sync"
	"time"
)

// A status sweep runs Codex probes concurrently. Sharing this short-lived
// snapshot avoids starting and parsing a full `ps` process table once per
// session while preserving quick discovery of newly spawned processes.
const codexProcessTableSnapshotTTL = time.Second

type processTableSnapshotCache struct {
	mu          sync.Mutex
	snapshot    []byte
	collectedAt time.Time
	ttl         time.Duration
}

func newProcessTableSnapshotCache(ttl time.Duration) *processTableSnapshotCache {
	return &processTableSnapshotCache{ttl: ttl}
}

func (c *processTableSnapshotCache) load(collect func() ([]byte, error)) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.snapshot) > 0 && time.Since(c.collectedAt) < c.ttl {
		return c.snapshot, nil
	}

	snapshot, err := collect()
	if err != nil {
		return nil, err
	}
	if len(snapshot) > 0 {
		c.snapshot = snapshot
		c.collectedAt = time.Now()
	}
	return snapshot, nil
}

var (
	codexProcessTableCache     = newProcessTableSnapshotCache(codexProcessTableSnapshotTTL)
	codexProcessTableCollector = func() ([]byte, error) {
		return exec.Command("ps", "-eo", "pid=,ppid=").Output()
	}
)

func loadCodexProcessTable() ([]byte, error) {
	return codexProcessTableCache.load(codexProcessTableCollector)
}
