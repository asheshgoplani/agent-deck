package sysinfo

import (
	"sync"
	"time"
)

// Collector runs a background goroutine that periodically collects system stats
// and caches them for consumption by all tmux sessions.
type Collector struct {
	mu       sync.RWMutex
	stats    Stats
	interval time.Duration
	stopCh   chan struct{}
	onUpdate func() // called after each collection cycle
}

// NewCollector creates a new stats collector. Call Start() to begin collection.
// onUpdate is called after each collection cycle (e.g., to refresh tmux status bars).
func NewCollector(intervalSeconds int, onUpdate func()) *Collector {
	if intervalSeconds <= 0 {
		intervalSeconds = 5
	}
	return &Collector{
		interval: time.Duration(intervalSeconds) * time.Second,
		stopCh:   make(chan struct{}),
		onUpdate: onUpdate,
	}
}

// Start begins the background collection goroutine.
func (c *Collector) Start() {
	// Probe GPU availability once at startup
	probeGPU()

	// Initial collection
	c.collect()

	go func() {
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				c.collect()
				if c.onUpdate != nil {
					c.onUpdate()
				}
			case <-c.stopCh:
				return
			}
		}
	}()
}

// Stop terminates the background collection goroutine.
func (c *Collector) Stop() {
	close(c.stopCh)
}

// Get returns the latest cached stats snapshot.
func (c *Collector) Get() Stats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.stats
}

// collect runs one cycle of stat collection.
func (c *Collector) collect() {
	stats := Stats{
		CPU:     collectCPU(),
		Memory:  collectMemory(),
		Disk:    collectDisk(),
		Load:    collectLoad(),
		GPU:     collectGPU(),
		Network: collectNetwork(),
	}

	c.mu.Lock()
	c.stats = stats
	c.mu.Unlock()
}

// Collect runs a single collection cycle and returns the result.
// Useful for testing without starting the background goroutine.
func Collect() Stats {
	return Stats{
		CPU:     collectCPU(),
		Memory:  collectMemory(),
		Disk:    collectDisk(),
		Load:    collectLoad(),
		GPU:     collectGPU(),
		Network: collectNetwork(),
	}
}
