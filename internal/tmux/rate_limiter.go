package tmux

import (
	"sync"
	"time"
)

// RateLimiter provides simple thread-safe rate limiting for events.
// It ensures that events are processed at most once per minimum interval.
type RateLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	lastExec time.Time
}

// NewRateLimiter creates a new rate limiter with the specified events per second.
func NewRateLimiter(eventsPerSecond int) *RateLimiter {
	if eventsPerSecond <= 0 {
		eventsPerSecond = 1
	}
	return &RateLimiter{
		interval: time.Second / time.Duration(eventsPerSecond),
	}
}

// Allow returns true if the event should be allowed based on the rate limit.
func (rl *RateLimiter) Allow() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	if now.Sub(rl.lastExec) >= rl.interval {
		rl.lastExec = now
		return true
	}
	return false
}

// Coalesce executes the provided callback only if the rate limit allows.
func (rl *RateLimiter) Coalesce(callback func()) {
	if rl.Allow() {
		callback()
	}
}
