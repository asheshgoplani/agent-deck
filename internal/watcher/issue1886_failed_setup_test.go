package watcher

import (
	"context"
	"errors"
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
type failedSetupAdapter struct {
	healthChecks atomic.Int64
	teardowns    atomic.Int64
}

func (a *failedSetupAdapter) Setup(context.Context, AdapterConfig) error {
	return errors.New("setup failed")
}

func (a *failedSetupAdapter) Listen(ctx context.Context, _ chan<- Event) error {
	<-ctx.Done()
	return ctx.Err()
}

func (a *failedSetupAdapter) Teardown() error {
	a.teardowns.Add(1)
	return nil
}

func (a *failedSetupAdapter) HealthCheck() error {
	a.healthChecks.Add(1)
	return nil
}

// TestEngine_HealthLoop_SkipsAdapterWithFailedSetup asserts the actual contract:
// the health loop must not call into an adapter whose Setup failed, and must
// still report that watcher as unhealthy.
func TestEngine_HealthLoop_SkipsAdapterWithFailedSetup(t *testing.T) {
	engine := newHealthLoopEngine(t, 20*time.Millisecond)

	adapter := &failedSetupAdapter{}
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

	engine.Stop()

	if n := adapter.teardowns.Load(); n != 0 {
		t.Errorf("Teardown called %d times on an adapter whose Setup failed, want 0", n)
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

// TestEngine_Stop_SkipsTeardownForFailedSetup verifies the symmetric case:
// Teardown releases what Setup allocated, so an adapter that never completed
// Setup must not be torn down either.
func TestEngine_Stop_SkipsTeardownForFailedSetup(t *testing.T) {
	engine, _ := newTestEngine(t, nil)

	adapter := &MockAdapter{setupErr: errors.New("setup failed")}
	engine.RegisterAdapter("w1", adapter, AdapterConfig{Type: "mock", Name: "broken"}, 60)

	if err := engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	engine.Stop()

	if !adapter.setupCalled {
		t.Fatal("Setup was never called")
	}
	if adapter.teardownCalled {
		t.Error("Teardown called on an adapter whose Setup failed")
	}
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
