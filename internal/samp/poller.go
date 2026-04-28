package samp

import (
	"sync"
	"time"
)

// SessionAlias pairs an agent-deck session id with its SAMP alias so the
// poller can map unread counts back to per-session badges.
type SessionAlias struct {
	SessionID string
	Alias     string
}

// Poller emits per-session unread counts on a fixed interval.
//
// One Poller per agent-deck process. The poller never writes to $DIR
// (no .seen-* mutation) — the agent itself owns the watermark via its
// /message-inbox slash command.
type Poller struct {
	// Dir is the SAMP message directory ($AGENT_MESSAGE_DIR or default).
	Dir string
	// Interval between sweeps. Zero defaults to 2s.
	Interval time.Duration
	// Sessions enumerates active (sessionID, alias) pairs each tick.
	// Entries with empty Alias are skipped.
	Sessions func() []SessionAlias
	// OnUpdate is called once per tick with the latest unread map
	// (sessionID → count). Sessions with zero unread are omitted.
	// Callers must treat the map as read-only and copy if mutating.
	OnUpdate func(map[string]int)

	mu    sync.Mutex
	cache map[string]*UnreadCache
	stop  chan struct{}
	done  chan struct{}
}

// Start begins polling in a background goroutine. Idempotent — calling
// Start on a running Poller is a no-op.
func (p *Poller) Start() {
	p.mu.Lock()
	if p.stop != nil {
		p.mu.Unlock()
		return
	}
	if p.Interval == 0 {
		p.Interval = 2 * time.Second
	}
	if p.cache == nil {
		p.cache = make(map[string]*UnreadCache)
	}
	p.stop = make(chan struct{})
	p.done = make(chan struct{})
	stop := p.stop
	done := p.done
	p.mu.Unlock()

	go func() {
		defer close(done)
		t := time.NewTicker(p.Interval)
		defer t.Stop()
		// Fire immediately so the first badge appears without waiting
		// one full interval.
		p.tick()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				p.tick()
			}
		}
	}()
}

// Stop signals the poller to exit and waits for the goroutine to drain.
// Safe to call on a never-started or already-stopped poller.
func (p *Poller) Stop() {
	p.mu.Lock()
	stop := p.stop
	done := p.done
	p.stop = nil
	p.done = nil
	p.mu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	if done != nil {
		<-done
	}
}

// tick performs one sweep. Exported indirectly for testing — callers
// outside the package use Start/Stop.
func (p *Poller) tick() {
	if p.Sessions == nil {
		return
	}
	sessions := p.Sessions()

	p.mu.Lock()
	if p.cache == nil {
		p.cache = make(map[string]*UnreadCache)
	}
	counts := make(map[string]int, len(sessions))
	keep := make(map[string]struct{}, len(sessions))
	for _, s := range sessions {
		if s.Alias == "" {
			continue
		}
		keep[s.Alias] = struct{}{}
		c, ok := p.cache[s.Alias]
		if !ok {
			c = &UnreadCache{}
			p.cache[s.Alias] = c
		}
		n, err := c.Get(p.Dir, s.Alias)
		if err != nil {
			continue
		}
		if n > 0 {
			counts[s.SessionID] = n
		}
	}
	// Drop cache entries for aliases that no longer exist so per-process
	// memory stays bounded across long runs with churning sessions.
	for k := range p.cache {
		if _, ok := keep[k]; !ok {
			delete(p.cache, k)
		}
	}
	p.mu.Unlock()

	if p.OnUpdate != nil {
		p.OnUpdate(counts)
	}
}
