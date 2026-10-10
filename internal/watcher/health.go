package watcher

import (
	"fmt"
	"sync"
	"time"
)

// HealthStatus is the overall health state of a watcher.
type HealthStatus string

const (
	// HealthStatusHealthy indicates the watcher is functioning normally.
	HealthStatusHealthy HealthStatus = "healthy"

	// HealthStatusWarning indicates a potential issue (e.g., silence or repeated errors).
	HealthStatusWarning HealthStatus = "warning"

	// HealthStatusError indicates the watcher is failing and requires attention.
	HealthStatusError HealthStatus = "error"
)

// HealthState is a snapshot of watcher health for display in the TUI and health alerts.
// It is emitted by HealthTracker.Check() and consumed by the engine in Phase 16.
type HealthState struct {
	// WatcherName is the name of the watcher this state belongs to
	WatcherName string

	// Status is the overall health status
	Status HealthStatus

	// EventsPerHour is the rolling event rate over the last 60 minutes
	EventsPerHour float64

	// LastEventTime is the time of the most recently received event
	LastEventTime time.Time

	// ConsecutiveErrors is the number of consecutive HealthCheck errors since the last event
	ConsecutiveErrors int

	// Message is a human-readable explanation for Warning or Error states
	Message string

	// Undelivered is the routed events of this watcher still waiting in
	// the send queue for their conductor, per conductor (#2537). Set by the
	// engine's health loop when it delivers through an Outbox.
	Undelivered []PendingDelivery `json:",omitempty"`
}

// HealthTracker is a passive (no goroutine) health monitor for a single watcher.
// It is called from the engine's health check loop via Check(), and updated
// via RecordEvent() and RecordError() as events flow through the engine.
//
// Thread-safe via mu; all public methods acquire appropriate locks.
type HealthTracker struct {
	mu sync.RWMutex

	watcherName string

	// lastEventTime is the time of the most recently recorded event.
	// Zero value means no event has been received yet.
	lastEventTime time.Time

	// consecutiveErrors tracks the number of consecutive HealthCheck failures
	// since the last successful RecordEvent call.
	consecutiveErrors int

	// eventTimestamps is a sliding window of recent event times for rolling rate calculation.
	// Pruned to only the last hour on each RecordEvent (T-13-03 mitigation).
	eventTimestamps []time.Time

	// maxSilenceMinutes is the threshold after which silence triggers a Warning.
	maxSilenceMinutes int

	// adapterHealthy tracks the last SetAdapterHealth value; defaults to true.
	adapterHealthy bool
}

// NewHealthTracker creates a HealthTracker for the named watcher.
// maxSilenceMinutes should come from WatcherSettings.GetMaxSilenceMinutes().
func NewHealthTracker(watcherName string, maxSilenceMinutes int) *HealthTracker {
	return &HealthTracker{
		watcherName:       watcherName,
		maxSilenceMinutes: maxSilenceMinutes,
		adapterHealthy:    true,
		eventTimestamps:   make([]time.Time, 0, 64),
	}
}

// RecordEvent records a successfully received event.
// Resets consecutiveErrors, updates lastEventTime, and prunes old timestamps
// from the sliding window (T-13-03: prevents unbounded memory growth).
func (h *HealthTracker) RecordEvent() {
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()

	h.lastEventTime = now
	h.consecutiveErrors = 0
	h.adapterHealthy = true

	// Append new timestamp and prune entries older than 1 hour
	h.eventTimestamps = append(h.eventTimestamps, now)
	cutoff := now.Add(-time.Hour)
	pruned := h.eventTimestamps[:0]
	for _, ts := range h.eventTimestamps {
		if ts.After(cutoff) {
			pruned = append(pruned, ts)
		}
	}
	h.eventTimestamps = pruned
}

// RecordError increments the consecutive error count.
// Call this when an adapter HealthCheck() returns an error.
func (h *HealthTracker) RecordError() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.consecutiveErrors++
}

// SetAdapterHealth records the adapter's current health status.
// Pass false when HealthCheck() fails; pass true when it recovers.
func (h *HealthTracker) SetAdapterHealth(healthy bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.adapterHealthy = healthy
}

// EventsInLastHour returns the number of events received within the past hour.
// This uses a lazy read approach; old entries are pruned on RecordEvent.
func (h *HealthTracker) EventsInLastHour() int {
	h.mu.RLock()
	defer h.mu.RUnlock()

	cutoff := time.Now().Add(-time.Hour)
	count := 0
	for _, ts := range h.eventTimestamps {
		if ts.After(cutoff) {
			count++
		}
	}
	return count
}

// Check evaluates the current health state and returns a HealthState snapshot.
// Per D-17, the status is computed as follows:
//   - Error: adapter unhealthy OR consecutiveErrors >= 10
//   - Warning: consecutiveErrors >= 3 OR silence beyond maxSilenceMinutes
//   - Healthy: otherwise
//
// The health check is passive (no I/O); the caller (engine) drives the check loop.
func (h *HealthTracker) Check() HealthState {
	h.mu.RLock()
	defer h.mu.RUnlock()

	eventsInHour := 0
	cutoff := time.Now().Add(-time.Hour)
	for _, ts := range h.eventTimestamps {
		if ts.After(cutoff) {
			eventsInHour++
		}
	}

	status := HealthStatusHealthy
	var message string

	switch {
	case !h.adapterHealthy || h.consecutiveErrors >= 10:
		status = HealthStatusError
		if !h.adapterHealthy {
			message = "adapter health check failed"
		} else {
			message = fmt.Sprintf("%d consecutive errors", h.consecutiveErrors)
		}
	case h.consecutiveErrors >= 3:
		status = HealthStatusWarning
		message = fmt.Sprintf("%d consecutive errors", h.consecutiveErrors)
	case !h.lastEventTime.IsZero() && time.Since(h.lastEventTime) > time.Duration(h.maxSilenceMinutes)*time.Minute:
		status = HealthStatusWarning
		elapsed := time.Since(h.lastEventTime).Round(time.Minute)
		message = fmt.Sprintf("no events for %v (threshold %d minutes)", elapsed, h.maxSilenceMinutes)
	}

	return HealthState{
		WatcherName:       h.watcherName,
		Status:            status,
		EventsPerHour:     float64(eventsInHour),
		LastEventTime:     h.lastEventTime,
		ConsecutiveErrors: h.consecutiveErrors,
		Message:           message,
	}
}

// SetLastEventTimeForTest allows tests to set lastEventTime directly for
// deterministic silence detection testing. Only intended for use in tests.
func (h *HealthTracker) SetLastEventTimeForTest(t time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastEventTime = t
}
