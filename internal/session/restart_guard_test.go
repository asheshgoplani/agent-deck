package session

import (
	"testing"
	"time"
)

// TestShouldSkipRestart_FreshHealthy reproduces issue #30: a session that was
// just started (healthy + fresh) must NOT be restarted, because the watchdog
// sometimes queues a redundant `session restart` right after a `session start`
// and the restart would tear down the just-created tmux/scope.
func TestShouldSkipRestart_FreshHealthy(t *testing.T) {
	now := time.Now()
	healthyStates := []Status{StatusRunning, StatusWaiting, StatusIdle, StatusStarting}
	for _, st := range healthyStates {
		inst := &Instance{
			Status:        st,
			LastStartedAt: now.Add(-30 * time.Second), // 30s ago, within freshness window
		}
		skip, reason := ShouldSkipRestart(inst, now, false)
		if !skip {
			t.Errorf("status=%s fresh: want skip=true, got skip=false", st)
		}
		if reason == "" {
			t.Errorf("status=%s fresh: want non-empty reason, got empty", st)
		}
	}
}

// TestShouldSkipRestart_StaleHealthy — a healthy session that's been running
// for a while (past the freshness window) is a legitimate restart target.
func TestShouldSkipRestart_StaleHealthy(t *testing.T) {
	now := time.Now()
	inst := &Instance{
		Status:        StatusRunning,
		LastStartedAt: now.Add(-5 * time.Minute),
	}
	skip, _ := ShouldSkipRestart(inst, now, false)
	if skip {
		t.Errorf("stale healthy session: want skip=false, got skip=true")
	}
}

// TestShouldSkipRestart_ErrorStatus — restarts on errored sessions always proceed.
func TestShouldSkipRestart_ErrorStatus(t *testing.T) {
	now := time.Now()
	inst := &Instance{
		Status:        StatusError,
		LastStartedAt: now.Add(-5 * time.Second), // fresh, but errored
	}
	skip, _ := ShouldSkipRestart(inst, now, false)
	if skip {
		t.Errorf("error status: want skip=false, got skip=true")
	}
}

// TestShouldSkipRestart_StoppedStatus — restarts on stopped sessions always proceed.
func TestShouldSkipRestart_StoppedStatus(t *testing.T) {
	now := time.Now()
	inst := &Instance{
		Status:        StatusStopped,
		LastStartedAt: now.Add(-5 * time.Second),
	}
	skip, _ := ShouldSkipRestart(inst, now, false)
	if skip {
		t.Errorf("stopped status: want skip=false, got skip=true")
	}
}

// TestShouldSkipRestart_Force — --force bypasses the freshness guard.
func TestShouldSkipRestart_Force(t *testing.T) {
	now := time.Now()
	inst := &Instance{
		Status:        StatusRunning,
		LastStartedAt: now.Add(-10 * time.Second),
	}
	skip, _ := ShouldSkipRestart(inst, now, true)
	if skip {
		t.Errorf("force=true: want skip=false, got skip=true")
	}
}

// TestShouldSkipRestart_UnknownStartTime — if LastStartedAt is zero (e.g. old
// session persisted before this field existed), proceed with the restart.
// Otherwise users could never recover such sessions via `session restart`.
func TestShouldSkipRestart_UnknownStartTime(t *testing.T) {
	now := time.Now()
	inst := &Instance{
		Status:        StatusRunning,
		LastStartedAt: time.Time{}, // zero value
	}
	skip, _ := ShouldSkipRestart(inst, now, false)
	if skip {
		t.Errorf("zero LastStartedAt: want skip=false, got skip=true")
	}
}

// TestShouldSkipRestart_ExactBoundary — at exactly 60s the session is no
// longer considered fresh.
func TestShouldSkipRestart_ExactBoundary(t *testing.T) {
	now := time.Now()
	inst := &Instance{
		Status:        StatusRunning,
		LastStartedAt: now.Add(-restartFreshnessWindow),
	}
	skip, _ := ShouldSkipRestart(inst, now, false)
	if skip {
		t.Errorf("at freshness boundary: want skip=false, got skip=true")
	}
}

// TestStart_RecordsLastStartedAt — the Instance.Start() code path must stamp
// LastStartedAt so a subsequent restart can consult it. Without this wiring
// the guard is a no-op and the #30 regression returns.
func TestStart_RecordsLastStartedAt(t *testing.T) {
	inst := &Instance{
		LastStartedAt: time.Time{},
	}
	before := time.Now()
	inst.markStarted() // helper encapsulating the LastStartedAt stamp
	after := time.Now()

	if inst.LastStartedAt.Before(before) || inst.LastStartedAt.After(after) {
		t.Fatalf("LastStartedAt=%v, want between %v and %v", inst.LastStartedAt, before, after)
	}
}
