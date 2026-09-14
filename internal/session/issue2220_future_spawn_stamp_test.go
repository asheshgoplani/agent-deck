package session

import (
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Issue #2220 regression suite — future-dated spawn stamp after a backwards
// clock correction.
//
// recordInstanceSpawn() used to carry its only signal in the stamp's mtime.
// If the wall clock is then corrected backwards (VM boots ahead, NTP pulls
// it back), the stamp sits in the future and spawnedSince() was true for
// every subsequent caller — Start() / Restart() / StartWithMessage() returned
// nil without creating a tmux session, and the CLI printed "Started session"
// over a silent no-op.
//
// Fix shape: the sibling-spawn gate is a generation counter stored in the
// stamp, snapshotted before the lock wait and compared after acquisition.
// Wall-clock ordering no longer decides anything, so a future-dated stamp
// cannot suppress a start, and a genuine sibling that stamped during our
// wait is still recognised even if the clock stepped backwards in between.
// The mtime is kept for `ls` triage; a future-dated one is logged once per
// stamp as a clock anomaly.

// stampWithOffset records a spawn for id and moves the stamp's mtime by
// offset from now.
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

// resetSpawnStampAnomalyLog swaps the anomaly log seam for a counter and
// clears the per-stamp dedupe set.
func resetSpawnStampAnomalyLog(t *testing.T) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	prevFn := spawnStampClockAnomalyLogFn
	spawnStampClockAnomalyLogFn = func(string, time.Time, time.Time) { calls.Add(1) }
	spawnStampClockAnomalySeen = sync.Map{}
	t.Cleanup(func() {
		spawnStampClockAnomalyLogFn = prevFn
		spawnStampClockAnomalySeen = sync.Map{}
	})
	return &calls
}

// pinNow replaces nowFn with a settable clock and restores it on cleanup.
func pinNow(t *testing.T, start time.Time) func(time.Time) {
	t.Helper()
	var mu sync.Mutex
	cur := start
	prev := nowFn
	nowFn = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return cur
	}
	t.Cleanup(func() { nowFn = prev })
	return func(when time.Time) {
		mu.Lock()
		defer mu.Unlock()
		cur = when
	}
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
// production entry points. A bare Instance with no tmux session reaches the
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

	// StartWithMessage() shares the same gate and the same nil-tmux error.
	// restart() is covered by the spawnedSince() unit tests below: with a
	// live tmux server it would recreate a real session for this bare
	// Instance, so it is not driven end-to-end here.
	stampWithOffset(t, inst.ID, time.Hour)
	err = inst.StartWithMessage("hello")
	if err == nil {
		t.Fatal("StartWithMessage() returned nil: future-dated stamp short-circuited the spawn")
	}
	if got := err.Error(); got != "tmux session not initialized" {
		t.Fatalf("StartWithMessage() error = %q, want the tmux-not-initialized error past the stamp gate", got)
	}
}

// TestSpawnAttempt_SiblingSpawnAcrossBackwardsClockStepStillRecognised is
// the other half of #2220: the guard must not buy future-stamp immunity by
// discarding genuine siblings. A sibling spawns and stamps while we wait
// for the lock; the clock then steps backwards by more than any skew
// tolerance before we acquire. The sibling's stamp is now "in the future",
// but it is still a spawn that happened during our wait, so Run() must
// skip Spawn. A wall-clock gate (with or without a clamp) gets this wrong.
func TestSpawnAttempt_SiblingSpawnAcrossBackwardsClockStepStillRecognised(t *testing.T) {
	withTempLockDir(t)
	resetSpawnStampAnomalyLog(t)

	base := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
	setNow := pinNow(t, base)

	const id = "inst-2220-sibling"
	prevAcquire := instanceSpawnLockAcquireFn
	t.Cleanup(func() { instanceSpawnLockAcquireFn = prevAcquire })
	instanceSpawnLockAcquireFn = func(instanceID string) (func(), error) {
		// While the caller is "waiting for the lock": a sibling spawns and
		// stamps at base+1s, then the clock is corrected back by five
		// seconds before the caller gets the lock.
		setNow(base.Add(time.Second))
		recordInstanceSpawn(instanceID)
		setNow(base.Add(-5 * time.Second))
		return func() {}, nil
	}

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
	if got := spawnCount.Load(); got != 0 {
		t.Fatalf("sibling spawned during our wait, then clock stepped back: spawn count = %d, want 0 (genuine sibling discarded)", got)
	}
}

// TestSpawnedSince_ClockAnomalyLoggedOncePerStamp: the warning is keyed by
// stamp (path + mtime), so two instances each get their own line, repeated
// reads of the same stamp do not, and a re-stamped file that is future-dated
// again is a new anomaly.
func TestSpawnedSince_ClockAnomalyLoggedOncePerStamp(t *testing.T) {
	withTempLockDir(t)
	calls := resetSpawnStampAnomalyLog(t)

	setNow := pinNow(t, time.Date(2026, 9, 10, 10, 56, 0, 0, time.UTC))

	stampWithOffset(t, "inst-2220-log-a", time.Hour)
	stampWithOffset(t, "inst-2220-log-b", time.Hour)

	noSpawn := func(id string) {
		t.Helper()
		// Spawn returns an error so the stamp is not rewritten and the
		// same anomalous stamp is observed on every call.
		attempt := SpawnAttempt{InstanceID: id, Spawn: func() error { return os.ErrClosed }}
		_ = attempt.Run()
	}
	for i := 0; i < 3; i++ {
		noSpawn("inst-2220-log-a")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("same stamp read 3 times: logged %d times, want 1", got)
	}
	noSpawn("inst-2220-log-b")
	if got := calls.Load(); got != 2 {
		t.Fatalf("second instance's anomalous stamp: logged %d times total, want 2 (once per stamp, not once per process)", got)
	}

	// Re-stamp a with a different future mtime: a new anomaly, logged again.
	setNow(nowFn().Add(time.Minute))
	stampWithOffset(t, "inst-2220-log-a", time.Hour)
	noSpawn("inst-2220-log-a")
	noSpawn("inst-2220-log-a")
	if got := calls.Load(); got != 3 {
		t.Fatalf("re-stamped future mtime: logged %d times total, want 3", got)
	}
}

// TestSpawnGeneration_GateIsClockIndependent pins the primitive: the gate
// compares stamp generations, so only a spawn recorded after the snapshot
// counts, whatever the stamp's mtime says. Legacy empty stamps read as
// generation 0 and never suppress.
func TestSpawnGeneration_GateIsClockIndependent(t *testing.T) {
	withTempLockDir(t)
	resetSpawnStampAnomalyLog(t)

	const id = "inst-2220-gen"
	if spawnedSince(id, spawnGenerationSnapshot(id)) {
		t.Fatal("no stamp: spawnedSince must be false")
	}

	before := spawnGenerationSnapshot(id)
	stampWithOffset(t, id, time.Hour) // sibling spawned, clock then stepped back
	if !spawnedSince(id, before) {
		t.Fatal("generation advanced after snapshot: spawnedSince must be true even with a future mtime")
	}
	if spawnedSince(id, spawnGenerationSnapshot(id)) {
		t.Fatal("snapshot taken after the spawn: spawnedSince must be false (legitimate sequential restart)")
	}

	// Pre-#2220 stamp: empty body with a future mtime.
	path, _ := instanceSpawnStampPath(id)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	future := nowFn().Add(time.Hour)
	_ = os.Chtimes(path, future, future)
	if spawnedSince(id, 0) {
		t.Fatal("legacy empty stamp reads as generation 0 and must not suppress")
	}
	before = spawnGenerationSnapshot(id)
	recordInstanceSpawn(id)
	if !spawnedSince(id, before) {
		t.Fatal("recordInstanceSpawn on a legacy stamp must advance the generation")
	}
}
