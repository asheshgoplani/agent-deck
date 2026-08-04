package session

import (
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Codex status refreshes run concurrently. Cache the tmux environment sweep
// briefly so every card in one sweep does not repeat it.
const codexSessionIDsSnapshotTTL = time.Second

type codexSessionIDsCache struct {
	mu          sync.Mutex
	snapshot    map[string]string
	collectedAt time.Time
	ttl         time.Duration
}

func newCodexSessionIDsCache(ttl time.Duration) *codexSessionIDsCache {
	return &codexSessionIDsCache{ttl: ttl}
}

func (c *codexSessionIDsCache) load(collect func() (map[string]string, error)) (map[string]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.snapshot != nil && time.Since(c.collectedAt) < c.ttl {
		return c.snapshot, nil
	}

	snapshot, err := collect()
	if err != nil {
		return nil, err
	}
	c.snapshot = snapshot
	c.collectedAt = time.Now()
	return snapshot, nil
}

var (
	codexSessionIDsSnapshot = newCodexSessionIDsCache(codexSessionIDsSnapshotTTL)
	collectCodexSessionIDs  = func() (map[string]string, error) {
		sessions, err := tmux.ListAgentDeckSessions()
		if err != nil {
			return nil, err
		}
		ids := make(map[string]string, len(sessions))
		for _, name := range sessions {
			sess := &tmux.Session{Name: name}
			if id, err := sess.GetEnvironment("CODEX_SESSION_ID"); err == nil && id != "" {
				ids[name] = id
			}
		}
		return ids, nil
	}
)

func loadCodexSessionIDs() (map[string]string, error) {
	return codexSessionIDsSnapshot.load(collectCodexSessionIDs)
}
