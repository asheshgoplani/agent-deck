package costs

import (
	"crypto/sha256"
	"sync"
)

type deduplicator struct {
	mu   sync.Mutex
	seen map[string][32]byte
}

func newDeduplicator() *deduplicator {
	return &deduplicator{seen: make(map[string][32]byte)}
}

func (d *deduplicator) isSeen(sessionID string, hash [32]byte) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	last, ok := d.seen[sessionID]
	return ok && last == hash
}

func (d *deduplicator) mark(sessionID string, hash [32]byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen[sessionID] = hash
}

// CostPoller polls tmux capture-pane for non-hook tools and extracts cost events.
type CostPoller struct {
	collector *Collector
	dedup     *deduplicator
}

// NewCostPoller creates a poller with deduplication.
func NewCostPoller(collector *Collector) *CostPoller {
	return &CostPoller{
		collector: collector,
		dedup:     newDeduplicator(),
	}
}

// Poll processes captured tmux output. Returns new cost events or nil if already seen.
func (p *CostPoller) Poll(toolType, sessionID, capturedOutput string) ([]CostEvent, error) {
	hash := sha256.Sum256([]byte(capturedOutput))
	if p.dedup.isSeen(sessionID, hash) {
		return nil, nil
	}
	p.dedup.mark(sessionID, hash)
	return p.collector.Collect(toolType, sessionID, capturedOutput)
}
