package watcher

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// Issue #1886: a watcher whose Setup failed stayed registered on the engine, and
// the health loop kept calling HealthCheck on it every tick. For the ntfy and
// slack adapters that means dereferencing an *http.Client that Setup never got
// far enough to build, which panicked and took the whole agent-deck process down
// on a timer.

// newHealthLoopEngine builds an engine with the health loop enabled; the shared
// newTestEngine helper sets HealthCheckInterval to 0, which disables it.
func newHealthLoopEngine(t *testing.T, interval time.Duration) *Engine {
	t.Helper()
	cfg := EngineConfig{
		DB:                  newTestDB(t),
		Router:              NewRouter(nil),
		MaxEventsPerWatcher: 500,
		HealthCheckInterval: interval,
		// Use fakeSpawner so tests never exec real agent-deck subprocesses.
		TriageSpawner: &fakeSpawner{},
		TriageDir:     t.TempDir(),
		ClientsPath:   filepath.Join(t.TempDir(), "clients.json"),
	}
	return NewEngine(cfg)
}

// failedSetupAdapter fails Setup and counts every call the engine makes into it
// afterwards. The counters are what tell "healthLoop skipped this adapter" apart
// from "healthLoop called it and got an error back" — a status assertion alone
// cannot, because the adapter-level nil-client guard also yields an error.
// HealthCheck returns nil on purpose: a call that did happen would flip the
// tracker to healthy, so it would fail the status assertion as well.
//
// failSetups bounds how many Setup attempts fail, so one adapter can model a
// retry that eventually succeeds. panicOnTeardown makes Teardown panic, which
// is the half-constructed behaviour cleanup has to survive. healthDelay slows
// HealthCheck down so a test can keep healthLoop inside its per-entry loop.
type failedSetupAdapter struct {
	failSetups      int64 // negative means: fail every attempt
	panicOnTeardown bool
	healthDelay     time.Duration

	setups       atomic.Int64
	healthChecks atomic.Int64
	teardowns    atomic.Int64
}

func (a *failedSetupAdapter) Setup(context.Context, AdapterConfig) error {
	n := a.setups.Add(1)
	if a.failSetups < 0 || n <= a.failSetups {
		return errors.New("setup failed")
	}
	return nil
}

func (a *failedSetupAdapter) Listen(ctx context.Context, _ chan<- Event) error {
	<-ctx.Done()
	return ctx.Err()
}

func (a *failedSetupAdapter) Teardown() error {
	a.teardowns.Add(1)
	if a.panicOnTeardown {
		panic("teardown on a half-constructed adapter")
	}
	return nil
}

func (a *failedSetupAdapter) HealthCheck() error {
	a.healthChecks.Add(1)
	if a.healthDelay > 0 {
		time.Sleep(a.healthDelay)
	}
	return nil
}

// TestEngine_HealthLoop_SkipsAdapterWithFailedSetup asserts the actual contract:
// the health loop must not call into an adapter whose Setup failed, and must
// still report that watcher as unhealthy.
func TestEngine_HealthLoop_SkipsAdapterWithFailedSetup(t *testing.T) {
	engine := newHealthLoopEngine(t, 20*time.Millisecond)

	adapter := &failedSetupAdapter{failSetups: -1}
	engine.RegisterAdapter("w1", adapter, AdapterConfig{Type: "mock", Name: "broken"}, 60)

	if err := engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for a state, then let several more ticks pass so a health loop that
	// probes unconditionally has every chance to call the adapter.
	select {
	case state := <-engine.HealthCh():
		if state.WatcherName != "broken" {
			t.Errorf("health state for %q, want %q", state.WatcherName, "broken")
		}
		if state.Status != HealthStatusError {
			t.Errorf("status = %q, want %q (setup failed)", state.Status, HealthStatusError)
		}
	case <-time.After(2 * time.Second):
		engine.Stop()
		t.Fatal("no health state emitted for the watcher whose Setup failed")
	}
	time.Sleep(100 * time.Millisecond)

	if n := adapter.healthChecks.Load(); n != 0 {
		t.Errorf("HealthCheck called %d times on an adapter whose Setup failed, want 0", n)
	}
	// The failed attempt is cleaned up at the moment it fails, not deferred to
	// Stop: Setup is not transactional, so it may already hold resources.
	if n := adapter.teardowns.Load(); n != 1 {
		t.Errorf("Teardown called %d times after the failed Setup, want exactly 1", n)
	}

	engine.Stop()

	// Stop must not tear the same failed attempt down a second time.
	if n := adapter.teardowns.Load(); n != 1 {
		t.Errorf("Teardown called %d times after Stop, want it to stay at 1 (exactly-once)", n)
	}
}

// TestEngine_HealthLoop_NtfyWithFailedSetupDoesNotPanic is the #1886 regression
// case itself: an ntfy watcher registered without a topic fails Setup before it
// assigns a.client, and the next health tick used to panic with a nil-pointer
// dereference inside net/http.(*Client).do, killing the process. Kept alongside
// the skip test above because only this one exercises the real adapter.
func TestEngine_HealthLoop_NtfyWithFailedSetupDoesNotPanic(t *testing.T) {
	engine := newHealthLoopEngine(t, 20*time.Millisecond)

	engine.RegisterAdapter("w1", &NtfyAdapter{}, AdapterConfig{
		Type:     "ntfy",
		Name:     "test-ntfy",
		Settings: map[string]string{},
	}, 60)

	if err := engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	// Before the fix the health loop panicked on the first tick and the test
	// binary died here rather than reporting a state.
	select {
	case state := <-engine.HealthCh():
		if state.WatcherName != "test-ntfy" {
			t.Errorf("health state for %q, want %q", state.WatcherName, "test-ntfy")
		}
		if state.Status != HealthStatusError {
			t.Errorf("status = %q, want %q (setup failed)", state.Status, HealthStatusError)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no health state emitted for the watcher whose Setup failed")
	}
}

// TestEngine_Stop_TearsDownSuccessfulSetupExactlyOnce covers the other side:
// an adapter that did complete Setup is holding resources, so Stop must release
// them — once, even if Stop is called again.
func TestEngine_Stop_TearsDownSuccessfulSetupExactlyOnce(t *testing.T) {
	engine, _ := newTestEngine(t, nil)

	adapter := &failedSetupAdapter{} // failSetups == 0: every Setup succeeds
	engine.RegisterAdapter("w1", adapter, AdapterConfig{Type: "mock", Name: "healthy"}, 60)

	if err := engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if n := adapter.teardowns.Load(); n != 0 {
		t.Fatalf("Teardown called %d times while the adapter was live, want 0", n)
	}

	engine.Stop()
	if n := adapter.teardowns.Load(); n != 1 {
		t.Errorf("Teardown called %d times after Stop, want exactly 1", n)
	}

	engine.Stop()
	if n := adapter.teardowns.Load(); n != 1 {
		t.Errorf("Teardown called %d times after a second Stop, want it to stay at 1", n)
	}
}

// TestEngine_SetupRetry_CleansUpEveryFailedAttempt is the retry case the review
// asked for: Setup is not transactional, so every failed attempt has to release
// what that attempt took, and a later success must leave exactly one live Setup
// for Stop to release. Two failures then a success means three Teardowns in
// total — one per failed attempt, one final.
func TestEngine_SetupRetry_CleansUpEveryFailedAttempt(t *testing.T) {
	engine, _ := newTestEngine(t, nil)

	adapter := &failedSetupAdapter{failSetups: 2}
	engine.RegisterAdapter("w1", adapter, AdapterConfig{Type: "mock", Name: "flaky"}, 60)
	entry := &engine.adapters[0]

	for attempt := 1; attempt <= 2; attempt++ {
		if ok := engine.setupAdapter(entry); ok {
			t.Fatalf("attempt %d: Setup unexpectedly succeeded", attempt)
		}
		if entry.setupOK {
			t.Errorf("attempt %d: setupOK is true after a failed Setup", attempt)
		}
		if n := adapter.teardowns.Load(); n != int64(attempt) {
			t.Errorf("attempt %d: Teardown called %d times, want %d (one per failed attempt)",
				attempt, n, attempt)
		}
	}

	if ok := engine.setupAdapter(entry); !ok {
		t.Fatal("third attempt: Setup should have succeeded")
	}
	if !entry.setupOK {
		t.Error("setupOK is false after a successful Setup")
	}
	if n := adapter.teardowns.Load(); n != 2 {
		t.Errorf("Teardown called %d times after the successful attempt, want it to stay at 2", n)
	}

	engine.Stop()

	if n := adapter.teardowns.Load(); n != 3 {
		t.Errorf("Teardown called %d times after Stop, want 3 (2 failed attempts + 1 final cleanup)", n)
	}
}

// TestEngine_TeardownPanic_IsContained pins the "panic-contained" half. Cleanup
// runs on the failed-Setup path, where the adapter is half-constructed — the
// exact state that panicked in #1886 — so a panicking Teardown must be
// swallowed rather than take the process down, and must not stop the engine
// from setting up the adapters that follow it.
func TestEngine_TeardownPanic_IsContained(t *testing.T) {
	engine, _ := newTestEngine(t, nil)

	panicky := &failedSetupAdapter{failSetups: -1, panicOnTeardown: true}
	healthy := &MockAdapter{}
	engine.RegisterAdapter("w1", panicky, AdapterConfig{Type: "mock", Name: "panicky"}, 60)
	engine.RegisterAdapter("w2", healthy, AdapterConfig{Type: "mock", Name: "healthy"}, 60)

	// Start must survive the panicking cleanup of the first adapter.
	if err := engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if n := panicky.teardowns.Load(); n != 1 {
		t.Errorf("Teardown called %d times on the failed adapter, want 1", n)
	}
	if !healthy.setupCalled {
		t.Error("the adapter after the panicking one was never set up")
	}

	// And Stop must still run to completion, tearing down the live adapter.
	engine.Stop()

	if !healthy.teardownCalled {
		t.Error("Teardown was not called on the live adapter during Stop")
	}
}

// TestEngine_Stop_DoesNotRaceWithHealthLoopOverSetupOK pins the shutdown half of
// the setupOK invariant. Stop's teardown pass runs after e.cancel() but before
// e.wg.Wait() returns, so healthLoop can still be mid-iteration reading setupOK
// while Stop walks the same entries: Stop must only read the field, never write
// it. stopOnce is what makes the pass run at most once, not clearing the flag.
//
// Deliberately shaped to make the window wide instead of hoping to hit it: each
// adapter's HealthCheck blocks for a millisecond, so with a 1ms tick healthLoop
// is inside its per-entry loop for tens of milliseconds while Stop's loop runs
// to completion in microseconds. Fails under -race if Stop writes setupOK.
func TestEngine_Stop_DoesNotRaceWithHealthLoopOverSetupOK(t *testing.T) {
	engine := newHealthLoopEngine(t, time.Millisecond)

	const adapters = 32
	for i := 0; i < adapters; i++ {
		engine.RegisterAdapter(
			fmt.Sprintf("w%d", i),
			&failedSetupAdapter{healthDelay: time.Millisecond}, // failSetups == 0: Setup succeeds
			AdapterConfig{Type: "mock", Name: fmt.Sprintf("live-%d", i)},
			60,
		)
	}

	if err := engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Let healthLoop get into an iteration before shutdown starts.
	time.Sleep(5 * time.Millisecond)

	engine.Stop()
}

// TestNtfy_HealthCheck_WithoutSetup pins the adapter-level guard: HealthCheck on
// an un-set-up adapter reports an error rather than panicking.
func TestNtfy_HealthCheck_WithoutSetup(t *testing.T) {
	if err := (&NtfyAdapter{}).HealthCheck(); err == nil {
		t.Error("HealthCheck() = nil, want an error when Setup never ran")
	}
}

// TestSlack_HealthCheck_WithoutSetup covers the same latent nil client in the
// slack adapter, which builds its client in Setup exactly like ntfy does.
func TestSlack_HealthCheck_WithoutSetup(t *testing.T) {
	if err := (&SlackAdapter{}).HealthCheck(); err == nil {
		t.Error("HealthCheck() = nil, want an error when Setup never ran")
	}
}
