package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// A send to a mid-turn Codex waits for the turn to end. It used to take the
// per-session acceptance lock first and hold it for that whole wait (up to
// --timeout), so every other send to the session failed after 5s with
// "Codex acceptance lock timed out" until the turn ended.
func TestCodexSendDoesNotHoldAcceptanceLockWhileWaitingForReady(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	const codexID = "thread-lock-after-ready"

	inReadyWait := make(chan struct{})
	turnEnded := make(chan struct{})
	waitReady := func(time.Duration) error {
		close(inReadyWait)
		<-turnEnded
		return nil
	}
	acquire := func(d time.Duration) (*codexAcceptanceGuard, error) {
		lock, err := session.AcquireCodexAcceptanceLock(codexID, d)
		if err != nil {
			return nil, err
		}
		return &codexAcceptanceGuard{lock: lock, fence: codexAcceptanceFence{priorTurnGeneration: "g1", available: true}}, nil
	}
	generation := func() (string, error) { return "g1", nil }

	done := make(chan error, 1)
	go func() {
		guard, readyErr, guardErr := acquireCodexAcceptanceWhenReady(time.Now().Add(time.Minute), time.Second, waitReady, generation, acquire)
		if guard != nil {
			guard.Release()
		}
		done <- errors.Join(readyErr, guardErr)
	}()

	<-inReadyWait
	other, err := session.AcquireCodexAcceptanceLock(codexID, 200*time.Millisecond)
	if err != nil {
		close(turnEnded)
		t.Fatalf("a send waiting for the turn to end held the acceptance lock: %v", err)
	}
	other.Release()
	close(turnEnded)
	if err := <-done; err != nil {
		t.Fatalf("send did not acquire after the turn ended: %v", err)
	}
}

// Both ways another send can win the race at turn end send this one back to
// waiting for readiness instead of refusing it.
func TestCodexAcceptanceWhenReadyWaitsAgainOnContention(t *testing.T) {
	for _, tc := range []struct {
		name string
		// first is the first acquisition's outcome; the second always succeeds.
		lockHeld, turnBegan bool
	}{
		{name: "lock held by a submitting send", lockHeld: true},
		{name: "a turn began while queued for the lock", turnBegan: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			gen, readyCalls, acquireCalls := "g1", 0, 0
			acquire := func(d time.Duration) (*codexAcceptanceGuard, error) {
				acquireCalls++
				if acquireCalls == 1 && tc.lockHeld {
					return nil, fmt.Errorf("%w after %s", session.ErrCodexAcceptanceLockTimeout, d)
				}
				if acquireCalls == 1 && tc.turnBegan {
					gen = "g2" // another send's turn started after readiness
				}
				lock, err := session.AcquireCodexAcceptanceLock("thread-contention", d)
				if err != nil {
					return nil, err
				}
				return &codexAcceptanceGuard{lock: lock, fence: codexAcceptanceFence{priorTurnGeneration: gen, available: true}}, nil
			}
			guard, readyErr, guardErr := acquireCodexAcceptanceWhenReady(time.Now().Add(time.Minute), time.Second,
				func(time.Duration) error { readyCalls++; return nil },
				func() (string, error) { return gen, nil },
				acquire)
			if readyErr != nil || guardErr != nil || guard == nil {
				t.Fatalf("guard=%v readyErr=%v guardErr=%v", guard, readyErr, guardErr)
			}
			guard.Release()
			if readyCalls != 2 || acquireCalls != 2 {
				t.Fatalf("readiness waits = %d, acquisitions = %d; want 2 and 2", readyCalls, acquireCalls)
			}
			if guard.fence.priorTurnGeneration != gen {
				t.Fatalf("fence %q is not the generation seen at readiness %q", guard.fence.priorTurnGeneration, gen)
			}
		})
	}
}

func TestCodexAcceptanceWhenReadyRefusesOtherGuardErrorsAtOnce(t *testing.T) {
	refusal := errors.New("unresolved earlier submission")
	readyCalls := 0
	_, readyErr, guardErr := acquireCodexAcceptanceWhenReady(time.Now().Add(time.Minute), time.Second,
		func(time.Duration) error { readyCalls++; return nil },
		func() (string, error) { return "g1", nil },
		func(time.Duration) (*codexAcceptanceGuard, error) { return nil, refusal })
	if readyErr != nil || !errors.Is(guardErr, refusal) || readyCalls != 1 {
		t.Fatalf("readyErr=%v guardErr=%v readyCalls=%d", readyErr, guardErr, readyCalls)
	}
}

func TestCodexAcceptanceWhenReadyEndsAtReadinessDeadline(t *testing.T) {
	notReady := errors.New("agent not ready after 1s")
	_, readyErr, guardErr := acquireCodexAcceptanceWhenReady(time.Now().Add(time.Minute), time.Second,
		func(time.Duration) error { return notReady },
		func() (string, error) { return "", nil },
		func(time.Duration) (*codexAcceptanceGuard, error) {
			t.Fatal("acquired the lock for a target that never became ready")
			return nil, nil
		})
	if !errors.Is(readyErr, notReady) || guardErr != nil {
		t.Fatalf("readyErr=%v guardErr=%v", readyErr, guardErr)
	}

	// Contention that outlasts the budget is a refusal (nothing was typed, so
	// the queue may retry), not a spin and not a readiness timeout.
	_, readyErr, guardErr = acquireCodexAcceptanceWhenReady(time.Now().Add(50*time.Millisecond), time.Second,
		func(time.Duration) error { time.Sleep(10 * time.Millisecond); return nil },
		func() (string, error) { return "", nil },
		func(time.Duration) (*codexAcceptanceGuard, error) {
			return nil, session.ErrCodexAcceptanceLockTimeout
		})
	if readyErr != nil || !errors.Is(guardErr, session.ErrCodexAcceptanceLockTimeout) {
		t.Fatalf("readyErr=%v guardErr=%v", readyErr, guardErr)
	}
}

// Readiness comes from a pane sample taken before waitReady returns. A turn
// that starts in that gap must not become this send's baseline, or the fence
// matches it and the send types into a running turn.
func TestCodexAcceptanceWhenReadyBaselinePrecedesTheWait(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	gen, readyCalls := "g1", 0
	guard, readyErr, guardErr := acquireCodexAcceptanceWhenReady(time.Now().Add(time.Minute), time.Second,
		func(time.Duration) error {
			readyCalls++
			if readyCalls == 1 {
				gen = "g2" // another sender's turn starts as readiness returns
			}
			return nil
		},
		func() (string, error) { return gen, nil },
		func(d time.Duration) (*codexAcceptanceGuard, error) {
			lock, err := session.AcquireCodexAcceptanceLock("thread-baseline", d)
			if err != nil {
				return nil, err
			}
			return &codexAcceptanceGuard{lock: lock, fence: codexAcceptanceFence{priorTurnGeneration: gen, available: true}}, nil
		})
	if readyErr != nil || guardErr != nil || guard == nil {
		t.Fatalf("guard=%v readyErr=%v guardErr=%v", guard, readyErr, guardErr)
	}
	guard.Release()
	if readyCalls != 2 {
		t.Fatalf("readiness waits = %d, want 2: the send accepted a turn that began during its wait", readyCalls)
	}
}

// The real waiter needs a few seconds on an idle pane, so with contention the
// budget usually runs out inside waitReady. That is still contention: nothing
// was typed, and the queue retries an acceptance refusal but not a readiness
// timeout.
func TestCodexAcceptanceWhenReadyContentionThenReadinessTimeoutRefuses(t *testing.T) {
	readyCalls := 0
	_, readyErr, guardErr := acquireCodexAcceptanceWhenReady(time.Now().Add(time.Minute), time.Second,
		func(time.Duration) error {
			readyCalls++
			if readyCalls > 1 {
				return errors.New("agent not ready after 1s")
			}
			return nil
		},
		func() (string, error) { return "g1", nil },
		func(time.Duration) (*codexAcceptanceGuard, error) {
			return nil, session.ErrCodexAcceptanceLockTimeout
		})
	if readyErr != nil || !errors.Is(guardErr, session.ErrCodexAcceptanceLockTimeout) {
		t.Fatalf("readyErr=%v guardErr=%v", readyErr, guardErr)
	}
}

// A generation that cannot be read refuses before the wait, as it did when the
// fence was captured first, instead of surfacing as a readiness timeout.
func TestCodexAcceptanceWhenReadyUnavailableGenerationRefusesBeforeWaiting(t *testing.T) {
	_, readyErr, guardErr := acquireCodexAcceptanceWhenReady(time.Now().Add(time.Minute), time.Second,
		func(time.Duration) error {
			t.Fatal("waited for a target whose rollout generation is unavailable")
			return nil
		},
		func() (string, error) { return "", errors.New("no rollout") },
		func(time.Duration) (*codexAcceptanceGuard, error) {
			t.Fatal("took the lock without a generation")
			return nil, nil
		})
	if readyErr != nil || !errors.Is(guardErr, errCodexGenerationUnavailable) {
		t.Fatalf("readyErr=%v guardErr=%v", readyErr, guardErr)
	}
}

func TestCodexAcceptanceWhenReadyNoWaitAcquiresOnce(t *testing.T) {
	calls := 0
	_, readyErr, guardErr := acquireCodexAcceptanceWhenReady(time.Now().Add(time.Minute), time.Second, nil,
		func() (string, error) { return "", nil },
		func(time.Duration) (*codexAcceptanceGuard, error) {
			calls++
			return nil, session.ErrCodexAcceptanceLockTimeout
		})
	if readyErr != nil || !errors.Is(guardErr, session.ErrCodexAcceptanceLockTimeout) || calls != 1 {
		t.Fatalf("--no-wait must keep the single bounded lock attempt: readyErr=%v guardErr=%v calls=%d", readyErr, guardErr, calls)
	}
}
