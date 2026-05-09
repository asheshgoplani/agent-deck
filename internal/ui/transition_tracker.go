package ui

import (
	"log/slog"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// transitionTracker emits enriched status_changed records and synthesizes
// flicker_detected WARN + session_status_cascade INFO summaries.
//
// Closes logging-review gaps:
//   - G4/G5/G6: status_changed at INFO with instance_id, tool, prev_for_ms.
//   - G6 synthesis: flicker_detected WARN when ≥flickerCount transitions
//     happen inside flickerWindow for the same instance.
//   - G7: session_status_cascade INFO when ≥cascadeCount transitions land
//     in a single status-loop tick.
type transitionTracker struct {
	mu sync.Mutex

	// per-instance transition history (timestamps of recent transitions)
	history map[string][]time.Time

	// per-instance last-transition time (for prev_for_ms)
	lastAt map[string]time.Time

	// per-instance time we last fired flicker_detected (for re-arm logic)
	lastFlickerFiredAt map[string]time.Time

	// per-tick counters (reset by tickEnd)
	tickKinds map[string]int // "old->new" -> count
	tickTotal int
}

// Tunables. Promoted to const to keep the helper non-configurable for v1.9.
const (
	flickerWindow      = 60 * time.Second
	flickerCount       = 3
	flickerReArmAfter  = 60 * time.Second
	cascadeMinPerTick  = 10
	historyTrimMaxKeep = 16
)

func newTransitionTracker() *transitionTracker {
	return &transitionTracker{
		history:            make(map[string][]time.Time),
		lastAt:             make(map[string]time.Time),
		lastFlickerFiredAt: make(map[string]time.Time),
		tickKinds:          make(map[string]int),
	}
}

// getTransitionTracker returns the home model's tracker, lazy-initializing
// on first use. Safe to call from multiple goroutines.
func (h *Home) getTransitionTracker() *transitionTracker {
	h.transitionTrackerOnce.Do(func() {
		h.transitionTracker = newTransitionTracker()
	})
	return h.transitionTracker
}

// record is the production entry-point: emits a status_changed log line and
// updates flicker history. Use the time.Now() clock.
func (tr *transitionTracker) record(instanceID, title, tool string, oldStatus, newStatus string) {
	tr.recordAt(instanceID, title, tool, oldStatus, newStatus, time.Now())
}

// recordAt is the testable variant accepting an explicit clock. Same emission
// shape as record().
func (tr *transitionTracker) recordAt(instanceID, title, tool, oldStatus, newStatus string, now time.Time) {
	tr.mu.Lock()
	prev := tr.lastAt[instanceID]
	var prevForMs int64
	if !prev.IsZero() {
		prevForMs = now.Sub(prev).Milliseconds()
	}
	tr.lastAt[instanceID] = now

	// Update flicker history.
	hist := append(tr.history[instanceID], now)
	// Drop entries outside the window.
	cutoff := now.Add(-flickerWindow)
	trimmed := hist[:0]
	for _, t := range hist {
		if t.After(cutoff) {
			trimmed = append(trimmed, t)
		}
	}
	if len(trimmed) > historyTrimMaxKeep {
		trimmed = trimmed[len(trimmed)-historyTrimMaxKeep:]
	}
	tr.history[instanceID] = trimmed

	// Cascade tally for the current tick.
	kind := oldStatus + "->" + newStatus
	tr.tickKinds[kind]++
	tr.tickTotal++

	// Decide whether to fire flicker_detected.
	fire := false
	if len(trimmed) >= flickerCount {
		lastFire := tr.lastFlickerFiredAt[instanceID]
		if lastFire.IsZero() || now.Sub(lastFire) >= flickerReArmAfter {
			fire = true
			tr.lastFlickerFiredAt[instanceID] = now
		}
	}
	count := len(trimmed)
	tr.mu.Unlock()

	notifLog.Info("status_changed",
		slog.String("instance_id", instanceID),
		slog.String("title", title),
		slog.String("tool", tool),
		slog.String("old", oldStatus),
		slog.String("new", newStatus),
		slog.Int64("prev_for_ms", prevForMs),
	)

	if fire {
		notifLog.Warn("flicker_detected",
			slog.String("instance_id", instanceID),
			slog.String("title", title),
			slog.Int("count", count),
			slog.Duration("window", flickerWindow),
			slog.String("latest", oldStatus+"->"+newStatus),
		)
	}
}

// tickEnd is called by the home-loop after a status pass completes.
// If the loop produced ≥cascadeMinPerTick transitions, emit a single INFO
// summary so a 28-session error→waiting storm is one log line, not 28.
func (tr *transitionTracker) tickEnd(tickStart, tickEnd time.Time) {
	tr.mu.Lock()
	total := tr.tickTotal
	kinds := tr.tickKinds
	tr.tickKinds = make(map[string]int)
	tr.tickTotal = 0
	tr.mu.Unlock()

	if total < cascadeMinPerTick {
		return
	}

	// Determine if the cascade is mostly one kind.
	dominantKind := ""
	dominantCount := 0
	for k, c := range kinds {
		if c > dominantCount {
			dominantKind = k
			dominantCount = c
		}
	}
	sameKind := dominantCount == total

	logging.Logger().Info("session_status_cascade",
		slog.String("component", logging.CompNotif),
		slog.Int("count", total),
		slog.Bool("same_kind", sameKind),
		slog.String("kind", dominantKind),
		slog.Int64("dur_ms", tickEnd.Sub(tickStart).Milliseconds()),
	)
}
