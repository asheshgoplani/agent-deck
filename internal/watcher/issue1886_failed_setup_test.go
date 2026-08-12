package watcher

import (
	"errors"
	"path/filepath"
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

// TestEngine_HealthLoop_SkipsAdapterWithFailedSetup reproduces #1886: an ntfy
// watcher registered without a topic fails Setup, and the next health tick used
// to panic with a nil-pointer dereference inside net/http.(*Client).do. The
// engine must report the watcher as unhealthy instead of probing it.
func TestEngine_HealthLoop_SkipsAdapterWithFailedSetup(t *testing.T) {
	engine := newHealthLoopEngine(t, 20*time.Millisecond)

	// No "topic" setting: NtfyAdapter.Setup returns an error before it assigns
	// a.client, which is exactly the state the panicking watcher was left in.
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
