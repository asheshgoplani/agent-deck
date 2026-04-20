package session

import (
	"fmt"
	"time"
)

// restartFreshnessWindow is the grace period during which a healthy session
// that was just started is considered "too fresh to restart". Issue #30: the
// watchdog previously queued a redundant `session restart` seconds after a
// manual `session start`, and the restart tore down the just-created tmux
// scope. 60 seconds covers typical watchdog tick + queue latency without
// meaningfully blocking legitimate rapid recycles.
const restartFreshnessWindow = 60 * time.Second

// ShouldSkipRestart decides whether `agent-deck session restart` should
// short-circuit instead of calling Instance.Restart().
//
// Returns skip=true (with a human-readable reason) when the session is in a
// healthy state AND was started within restartFreshnessWindow. The force
// parameter (wired to the CLI --force flag) bypasses the guard.
//
// A zero LastStartedAt is treated as "unknown" — we always permit the
// restart so users can recover legacy records.
func ShouldSkipRestart(inst *Instance, now time.Time, force bool) (bool, string) {
	if inst == nil || force {
		return false, ""
	}
	if inst.LastStartedAt.IsZero() {
		return false, ""
	}
	if !isHealthyForFreshnessGuard(inst.Status) {
		return false, ""
	}
	age := now.Sub(inst.LastStartedAt)
	if age >= restartFreshnessWindow {
		return false, ""
	}
	return true, fmt.Sprintf(
		"session is %s and was started %s ago (within %s freshness window); use --force to restart anyway",
		inst.Status, age.Round(time.Second), restartFreshnessWindow,
	)
}

// isHealthyForFreshnessGuard reports whether the status indicates the session
// is alive enough that a restart would be destructive.
func isHealthyForFreshnessGuard(s Status) bool {
	switch s {
	case StatusRunning, StatusWaiting, StatusIdle, StatusStarting:
		return true
	default:
		return false
	}
}

// markStarted stamps LastStartedAt with the current wall-clock time. It is
// the single source of truth for the persisted start timestamp so tests and
// callers don't drift.
func (i *Instance) markStarted() {
	i.LastStartedAt = time.Now()
}
