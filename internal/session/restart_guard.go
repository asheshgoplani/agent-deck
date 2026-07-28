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
// Two independent guards, both bypassed by force (the CLI --force flag):
//
//  1. AUTH HOLD — the session's agent could not authenticate. A restart cannot
//     fix a credential, and each doomed boot races the single rotating refresh
//     token shared by every session on the host, which is how one expired token
//     became a fleet-wide outage on 2026-07-26. Checked FIRST: it applies
//     regardless of freshness, and it is the more consequential of the two.
//
//     This guard deliberately lives on the CLI path only. `session restart <id>`
//     is what watchdogs, cron jobs and conductor sweeps call — the automation
//     that must not flap. A human pressing R in the TUI calls Instance.Restart()
//     in-process and is never gated: pressing R is the explicit "I fixed the
//     credentials" signal the hold is waiting for.
//
//  2. FRESHNESS (issue #30) — the session is healthy and was started within
//     restartFreshnessWindow, so a restart would tear down a just-created tmux
//     scope for nothing.
//
// A zero LastStartedAt is treated as "unknown" for the freshness guard — we
// always permit the restart so users can recover legacy records.
func ShouldSkipRestart(inst *Instance, now time.Time, force bool) (bool, string) {
	if inst == nil || force {
		return false, ""
	}
	if held, remedy := inst.IsAuthHeld(); held {
		return true, remedy + " (use --force to restart anyway)"
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
