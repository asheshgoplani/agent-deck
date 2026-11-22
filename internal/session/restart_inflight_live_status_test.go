package session

import (
	"testing"
	"time"
)

// The restart guard judges the live status (RefreshLiveStatus), so a row
// whose pane is gone reads error and is not skipped, even when it was started
// seconds ago. A restart that is still in flight has exactly that shape for a
// moment: it killed the old pane and has not created the new one yet. A second
// `session restart` (or a watchdog) arriving in that window must not start the
// session a second time. What prevents it is the per-instance spawn lock that
// restart() holds from before the kill to after the spawn, with its generation
// gate (#1040): the second restart waits for the lock, sees the first one's
// spawn generation, and spawns nothing (errRestartLostSpawn, reported as
// success by restartRecorded).
func TestRestartDuringAnInFlightRestartSpawnsNothing(t *testing.T) {
	skipIfNoTmuxBinary(t)
	withTempLockDir(t)

	inst := NewInstanceWithTool("inflight-restart", t.TempDir(), "shell")
	inst.CreatedAt = time.Now().Add(-time.Minute)
	if err := inst.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = inst.Kill() })
	if !inst.tmuxSession.Exists() {
		t.Fatal("precondition: started session has no tmux session")
	}

	// The first restart (A) is in flight: it holds the spawn lock and has
	// killed the old pane; the session was started seconds ago, past the
	// 1.5 s start grace.
	release, err := acquireInstanceSpawnLock(inst.ID)
	if err != nil {
		t.Fatalf("acquire spawn lock: %v", err)
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()
	if err := inst.tmuxSession.Kill(); err != nil {
		t.Fatalf("kill pane: %v", err)
	}
	inst.mu.Lock()
	inst.lastStartTime = time.Now().Add(-5 * time.Second)
	inst.mu.Unlock()
	inst.LastStartedAt = time.Now().Add(-5 * time.Second)

	// The second restart (B) judges the live status: the pane is gone, so
	// the freshness guard does not skip it.
	RefreshLiveStatus([]*Instance{inst})
	if got := inst.GetStatusThreadSafe(); got != StatusError {
		t.Fatalf("live status in the restart window = %q, want error", got)
	}
	if skip, reason := ShouldSkipRestart(inst, time.Now(), false); skip {
		t.Fatalf("guard skipped a row with no pane: %s", reason)
	}

	genBefore := spawnGenerationSnapshot(inst.ID)
	done := make(chan error, 1)
	go func() { done <- inst.Restart() }()
	select {
	case err := <-done:
		t.Fatalf("second restart did not wait for the in-flight one (err=%v)", err)
	case <-time.After(300 * time.Millisecond):
	}

	// A finishes its spawn and releases the lock.
	recordInstanceSpawn(inst.ID)
	released = true
	release()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second restart: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("second restart still blocked after the lock was released")
	}
	if got := spawnGenerationSnapshot(inst.ID); got != genBefore+1 {
		t.Fatalf("spawn generation = %d, want %d (only the in-flight restart's spawn)", got, genBefore+1)
	}
	if inst.tmuxSession != nil && inst.tmuxSession.Exists() {
		t.Fatalf("second restart created tmux session %q: the session was started twice", inst.tmuxSession.Name)
	}
}
