package session

import (
	"sync"
	"testing"
)

// TestApplyTerminatedPaneStatus_HoldsLockOnReturn verifies the lock handoff
// contract: applyTerminatedPaneStatus is entered with i.mu held, drops it for
// the (potentially slow) tmux exit-status probe, and must hold i.mu again on
// return so the caller's deferred Unlock stays balanced. With a nil tmux
// session no subprocess runs, so this exercises the unlock/reacquire path
// without a live tmux server.
func TestApplyTerminatedPaneStatus_HoldsLockOnReturn(t *testing.T) {
	i := &Instance{Tool: "claude", Status: StatusRunning}

	i.mu.Lock()
	i.applyTerminatedPaneStatus()

	// The helper must have reacquired the lock; TryLock on an already-held
	// mutex returns false.
	if i.mu.TryLock() {
		i.mu.Unlock()
		t.Fatal("applyTerminatedPaneStatus returned without holding i.mu")
	}
	got := i.Status
	i.mu.Unlock()

	// nil tmux → no exit code → per-tool heuristic; a hook-emitting tool's
	// vanished pane is a crash.
	if got != StatusError {
		t.Errorf("Status = %q, want %q", got, StatusError)
	}
}

// TestApplyTerminatedPaneStatus_StoppedGuard pins the stopped-state guard.
// Because i.mu is dropped during the probe, a concurrent Stop() may set
// StatusStopped while the query is in flight; the result must not clobber it.
// Here the session is already StatusStopped before the call, standing in for
// that window — the terminated-pane classification (which for "claude" would be
// StatusError) must be suppressed.
func TestApplyTerminatedPaneStatus_StoppedGuard(t *testing.T) {
	i := &Instance{Tool: "claude", Status: StatusStopped}

	i.mu.Lock()
	i.applyTerminatedPaneStatus()
	got := i.Status
	i.mu.Unlock()

	if got != StatusStopped {
		t.Errorf("Status = %q, want %q (stopped-state guard must hold)", got, StatusStopped)
	}
}

// TestApplyTerminatedPaneStatus_ConcurrentRaceSafe drives the helper from many
// goroutines contending on the same i.mu. It is a no-op assertion-wise; its
// value is under `go test -race`, which flags any unsynchronized access or a
// lock-handoff imbalance (double unlock / unbalanced reacquire).
func TestApplyTerminatedPaneStatus_ConcurrentRaceSafe(t *testing.T) {
	i := &Instance{Tool: "opencode", Status: StatusRunning}

	var wg sync.WaitGroup
	for n := 0; n < 50; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			i.mu.Lock()
			i.applyTerminatedPaneStatus()
			i.mu.Unlock()
		}()
	}
	wg.Wait()

	i.mu.Lock()
	got := i.Status
	i.mu.Unlock()
	// opencode's hookless vanished pane classifies as stopped.
	if got != StatusStopped {
		t.Errorf("Status = %q, want %q", got, StatusStopped)
	}
}
