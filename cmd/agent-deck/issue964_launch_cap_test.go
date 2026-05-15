package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestLaunch_RespectsMaxParallel_RegressionFor964 pins the fix for
// https://github.com/asheshgoplani/agent-deck/issues/964.
//
// Bug: parallel `agent-deck launch` calls cascade into swap thrash because
// each launch spawns claude (~1-2GB RSS, ~71GB VSZ) plus a go test runner /
// build (~1-2GB more). With 6+ concurrent launches the host runs out of
// physical RAM and committed virtual memory; the "stability" mitigation
// `vm.overcommit_memory=2` then blocks every subsequent fork.
//
// This regression test pins the structural fix: a process-wide semaphore
// caps the number of in-flight launches at a small default (3, matching
// the safe-launch convention). The cap is configurable via the
// AGENT_DECK_MAX_PARALLEL_LAUNCH env var.
func TestLaunch_RespectsMaxParallel_RegressionFor964(t *testing.T) {
	const cap = 3
	const callers = 5

	throttle := newLaunchThrottle(cap)

	var (
		inFlight atomic.Int32
		maxSeen  atomic.Int32
		wg       sync.WaitGroup
	)

	// Fire 5 launches simultaneously. Without the cap, all 5 would run in
	// parallel — the exact pattern that crashed the conductor twice.
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			throttle.Acquire()
			defer throttle.Release()

			cur := inFlight.Add(1)
			for {
				prev := maxSeen.Load()
				if cur <= prev || maxSeen.CompareAndSwap(prev, cur) {
					break
				}
			}
			// Simulate the heavy work (claude boot + go build).
			time.Sleep(40 * time.Millisecond)
			inFlight.Add(-1)
		}()
	}
	wg.Wait()

	if got := maxSeen.Load(); got > int32(cap) {
		t.Fatalf("max concurrent launches=%d exceeded cap=%d — throttle did not gate the spawn point", got, cap)
	}
	if got := maxSeen.Load(); got == 0 {
		t.Fatalf("throttle never observed any in-flight callers; instrumentation broken")
	}
}

// TestLaunchThrottleCap_DefaultAndEnvOverride_RegressionFor964 documents the
// default cap (3) and the AGENT_DECK_MAX_PARALLEL_LAUNCH env-var override.
// The default matches the safe-launch convention referenced in PROMPT.md.
func TestLaunchThrottleCap_DefaultAndEnvOverride_RegressionFor964(t *testing.T) {
	t.Run("default cap is 3", func(t *testing.T) {
		t.Setenv("AGENT_DECK_MAX_PARALLEL_LAUNCH", "")
		if got := launchThrottleCap(); got != 3 {
			t.Errorf("default launchThrottleCap()=%d, want 3", got)
		}
	})

	t.Run("env var overrides default", func(t *testing.T) {
		t.Setenv("AGENT_DECK_MAX_PARALLEL_LAUNCH", "5")
		if got := launchThrottleCap(); got != 5 {
			t.Errorf("with env=5, launchThrottleCap()=%d, want 5", got)
		}
	})

	t.Run("invalid env value falls back to default", func(t *testing.T) {
		t.Setenv("AGENT_DECK_MAX_PARALLEL_LAUNCH", "not-a-number")
		if got := launchThrottleCap(); got != 3 {
			t.Errorf("with garbage env, launchThrottleCap()=%d, want 3", got)
		}
	})

	t.Run("non-positive env value falls back to default", func(t *testing.T) {
		t.Setenv("AGENT_DECK_MAX_PARALLEL_LAUNCH", "0")
		if got := launchThrottleCap(); got != 3 {
			t.Errorf("with env=0, launchThrottleCap()=%d, want 3", got)
		}
	})
}
