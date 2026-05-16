package session

import (
	"log/slog"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// FlickerDetector observes status transitions per session and emits a
// synthetic "flicker_detected" WARN log when a session oscillates more
// than flickerThreshold times within flickerWindow.
//
// Motivation: today's six-flicker oscillation incident was logged at
// Debug only and provided no aggregate signal. Operators had to grep
// status_changed lines manually. A single WARN per burst per session
// is enough to trigger an alert without spamming logs during a sustained
// flicker storm (rate-limited via flickerCooldown).
type FlickerDetector struct {
	mu sync.Mutex
	// transitions[sessionID] is a sliding window of transition timestamps
	// within the last flickerWindow. Older entries are pruned on Observe.
	transitions map[string][]time.Time
	// lastWarned[sessionID] holds the time we last emitted flicker_detected
	// for this session, used to enforce flickerCooldown.
	lastWarned map[string]time.Time

	logger *slog.Logger
}

const (
	// flickerThreshold is the number of transitions in the window that
	// constitutes a flicker. Strict-greater-than: 4+ transitions trigger.
	flickerThreshold = 3
	// flickerWindow is the sliding window within which transitions count.
	flickerWindow = 60 * time.Second
	// flickerCooldown silences repeat warns for the same session while it
	// is still flickering. One alert per burst is enough.
	flickerCooldown = 60 * time.Second
)

// NewFlickerDetector creates a fresh detector. Tests construct their own;
// production uses GlobalFlickerDetector.
func NewFlickerDetector() *FlickerDetector {
	return &FlickerDetector{
		transitions: make(map[string][]time.Time),
		lastWarned:  make(map[string]time.Time),
		logger:      logging.ForComponent(logging.CompSession),
	}
}

var (
	globalFlickerOnce sync.Once
	globalFlicker     *FlickerDetector
)

// GlobalFlickerDetector returns the process-wide detector used by the TUI's
// status_changed call site.
func GlobalFlickerDetector() *FlickerDetector {
	globalFlickerOnce.Do(func() {
		globalFlicker = NewFlickerDetector()
	})
	return globalFlicker
}

// Observe records a status transition for sessionID at the current time.
// Callers should only invoke this when the new status differs from the
// previous status — Observe does not de-duplicate.
func (d *FlickerDetector) Observe(sessionID, status string) {
	d.observeAt(sessionID, status, time.Now())
}

// observeAt is the testable form of Observe.
func (d *FlickerDetector) observeAt(sessionID, status string, now time.Time) {
	if sessionID == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	cutoff := now.Add(-flickerWindow)
	filtered := d.transitions[sessionID][:0:0]
	for _, ts := range d.transitions[sessionID] {
		if ts.After(cutoff) {
			filtered = append(filtered, ts)
		}
	}
	filtered = append(filtered, now)
	d.transitions[sessionID] = filtered

	if len(filtered) <= flickerThreshold {
		return
	}

	if last, ok := d.lastWarned[sessionID]; ok && now.Sub(last) < flickerCooldown {
		return
	}
	d.lastWarned[sessionID] = now

	d.logger.Warn("flicker_detected",
		slog.String("session", sessionID),
		slog.String("latest_status", status),
		slog.Int("transitions", len(filtered)),
		slog.Duration("window", flickerWindow),
	)
}
