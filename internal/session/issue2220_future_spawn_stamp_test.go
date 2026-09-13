package session

import (
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// Issue #2220 regression suite — future-dated spawn stamp after a backwards
// clock correction.
//
// recordInstanceSpawn() sets the stamp mtime to "now". If the wall clock is
// then corrected backwards (VM boots ahead, NTP pulls it back), the stamp's
// mtime is in the future and spawnedSince() was true for every subsequent
// caller — Start() / Restart() / StartWithMessage() returned nil without
// creating a tmux session, and the CLI printed "Started session" over a
// silent no-op.
//
// Fix shape: a stamp is only evidence of a sibling spawn when its mtime lies
// in [beforeLock, now]. A small skew tolerance clamps mtime to now; anything
// further ahead is a clock anomaly (logged once, ignored). A stamp older than
// the freshness window is likewise ignored so the guard's suppression is
// bounded rather than open-ended.

// stampWithOffset writes the spawn stamp for id with its mtime offset from now.
func stampWithOffset(t *testing.T, id string, offset time.Duration) string {
	t.Helper()
	recordInstanceSpawn(id)
	path, err := instanceSpawnStampPath(id)
	if err != nil {
		t.Fatalf("stamp path: %v", err)
	}
	when := nowFn().Add(offset)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	return path
}

func resetSpawnStampAnomalyLog(t *testing.T) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	prevFn := spawnStampClockAnomalyLogFn
	spawnStampClockAnomalyLogFn = func(string, time.Time, time.Time) { calls.Add(1) }
	spawnStampClockAnomalyLogged.Store(false)
	t.Cleanup(func() {
		spawnStampClockAnomalyLogFn = prevFn
		spawnStampClockAnomalyLogged.Store(false)
	})
	return &calls
}

// TestSpawnAttempt_FutureStampDoesNotSuppressSpawn_RegressionFor2220 is the
// failing-first repro: a stamp dated one hour ahead must not make Run() skip
// Spawn. Pre-fix, spawnedSince() returned mtime.After(beforeLock) == true and
// the spawn count stayed at 0.
func TestSpawnAttempt_FutureStampDoesNotSuppressSpawn_RegressionFor2220(t *testing.T) {
	withTempLockDir(t)
	resetSpawnStampAnomalyLog(t)

	const id = "inst-2220-future"
	stampWithOffset(t, id, time.Hour)

	var spawnCount atomic.Int32
	attempt := SpawnAttempt{
		InstanceID: id,
		Spawn: func() error {
			spawnCount.Add(1)
			return nil
		},
	}
	if err := attempt.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := spawnCount.Load(); got != 1 {
		t.Fatalf("future-dated stamp: spawn count = %d, want 1 (guard suppressed a legitimate start)", got)
	}

	// A successful spawn re-stamps at now, so the anomaly self-heals: the
	// stamp must no longer sit in the future.
	path, _ := instanceSpawnStampPath(id)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat stamp: %v", err)
	}
	if info.ModTime().After(nowFn().Add(time.Second)) {
		t.Fatalf("stamp still future-dated after spawn: mtime=%s now=%s", info.ModTime(), nowFn())
	}
}

// TestInstanceStart_FutureStampDoesNotShortCircuit_RegressionFor2220 pins the
// production entry point. A bare Instance with no tmux session reaches the
// "tmux session not initialized" error only if the spawn guard lets Start()
// past the stamp gate; pre-fix the future stamp made Start() return nil.
func TestInstanceStart_FutureStampDoesNotShortCircuit_RegressionFor2220(t *testing.T) {
	withTempLockDir(t)
	resetSpawnStampAnomalyLog(t)

	inst := &Instance{ID: "inst-2220-start"}
	stampWithOffset(t, inst.ID, time.Hour)

	err := inst.Start()
	if err == nil {
		t.Fatal("Start() returned nil: future-dated stamp short-circuited the spawn (silent no-op)")
	}
	if got := err.Error(); got != "tmux session not initialized" {
		t.Fatalf("Start() error = %q, want the tmux-not-initialized error past the stamp gate", got)
	}

	// Restart() shares the same gate.
	stampWithOffset(t, inst.ID, time.Hour)
	if err := inst.Restart(); err == nil {
		t.Fatal("Restart() returned nil: future-dated stamp short-circuited the respawn")
	}
}

// TestSpawnedSince_FutureStampClampBound pins the clamp boundary: a stamp
// within the skew tolerance is treated as written at "now" (still a sibling
// spawn, still suppresses), one beyond the tolerance is a clock anomaly and
// never suppresses. The 1h case is the #2220 field shape.
func TestSpawnedSince_FutureStampClampBound(t *testing.T) {
	withTempLockDir(t)
	resetSpawnStampAnomalyLog(t)

	const id = "inst-2220-bound"
	cases := []struct {
		name   string
		offset time.Duration
		want   bool
	}{
		{"just written (past)", -10 * time.Millisecond, true},
		{"within tolerance clamps to now", instanceSpawnStampSkewTolerance / 2, true},
		{"one second past tolerance", instanceSpawnStampSkewTolerance + time.Second, false},
		{"one hour ahead (field repro)", time.Hour, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stampWithOffset(t, id, tc.offset)
			ref := nowFn().Add(-time.Second)
			if got := spawnedSince(id, ref); got != tc.want {
				t.Fatalf("spawnedSince(offset=%s) = %v, want %v", tc.offset, got, tc.want)
			}
		})
	}
}

// TestSpawnedSince_StaleStampBeyondFreshnessWindowIgnored bounds the other
// side: the guard only ever needs to cover a sibling that stamped during our
// lock wait, so a stamp older than the freshness window is not evidence of
// one even when it post-dates the reference.
func TestSpawnedSince_StaleStampBeyondFreshnessWindowIgnored(t *testing.T) {
	withTempLockDir(t)
	resetSpawnStampAnomalyLog(t)

	const id = "inst-2220-stale"
	stampWithOffset(t, id, -(instanceSpawnStampFreshness + time.Minute))
	ref := nowFn().Add(-(instanceSpawnStampFreshness + 2*time.Minute))
	if spawnedSince(id, ref) {
		t.Fatal("stamp older than the freshness window suppressed a spawn")
	}

	stampWithOffset(t, id, -(instanceSpawnStampFreshness / 2))
	if !spawnedSince(id, ref) {
		t.Fatal("stamp inside the freshness window and after ref must still count as a sibling spawn")
	}
}

// TestSpawnedSince_ClockAnomalyLoggedOnce: the anomaly is worth one warning
// per process, not one per poll tick.
func TestSpawnedSince_ClockAnomalyLoggedOnce(t *testing.T) {
	withTempLockDir(t)
	calls := resetSpawnStampAnomalyLog(t)

	const id = "inst-2220-log"
	stampWithOffset(t, id, time.Hour)
	ref := nowFn()
	for i := 0; i < 3; i++ {
		if spawnedSince(id, ref) {
			t.Fatalf("call %d: future stamp suppressed", i)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("clock anomaly logged %d times, want exactly 1", got)
	}
}
