package watcher

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type healthPanicAdapter struct {
	panics atomic.Bool
	checks atomic.Int32
	passes atomic.Int32
}

func (*healthPanicAdapter) Setup(context.Context, AdapterConfig) error { return nil }
func (*healthPanicAdapter) Listen(context.Context, chan<- Event) error { return nil }
func (*healthPanicAdapter) Teardown() error                            { return nil }
func (a *healthPanicAdapter) HealthCheck() error {
	a.checks.Add(1)
	if a.panics.Load() {
		panic("secret-health-panic-value")
	}
	a.passes.Add(1)
	return nil
}

type healthCountingAdapter struct{ checks atomic.Int32 }

func (*healthCountingAdapter) Setup(context.Context, AdapterConfig) error { return nil }
func (*healthCountingAdapter) Listen(context.Context, chan<- Event) error { return nil }
func (*healthCountingAdapter) Teardown() error                            { return nil }
func (a *healthCountingAdapter) HealthCheck() error {
	a.checks.Add(1)
	return nil
}

func TestRecoveredAdapterContainsHealthPanicAndSanitizesLog(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	engine := NewEngine(EngineConfig{Logger: logger, HealthCheckInterval: time.Millisecond})
	panicking := &healthPanicAdapter{}
	panicking.panics.Store(true)
	sibling := &healthCountingAdapter{}
	engine.RegisterAdapter("panic-id", panicking, AdapterConfig{Name: "panic-watch", Type: "test"}, 0)
	engine.RegisterAdapter("sibling-id", sibling, AdapterConfig{Name: "sibling-watch", Type: "test"}, 0)

	if err := engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(engine.Stop)

	wantStates(t, engine.HealthCh(), map[string]HealthStatus{
		"panic-watch":   HealthStatusError,
		"sibling-watch": HealthStatusHealthy,
	})
	if sibling.checks.Load() == 0 {
		t.Fatal("healthy sibling was not checked by the engine health loop")
	}
	panicState := engine.adapters[0].tracker.Check()
	if panicState.Status != HealthStatusError || panicState.ConsecutiveErrors == 0 {
		t.Fatalf("panic tracker state = %+v, want unhealthy with a recorded error", panicState)
	}

	// Repeated panics remain unhealthy without emitting a full stack every tick.
	deadline := time.Now().Add(time.Second)
	for panicking.checks.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	// A successful check ends the episode, but a quick later transition remains
	// inside the stack-log rate cap.
	panicking.panics.Store(false)
	deadline = time.Now().Add(time.Second)
	for panicking.passes.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if panicking.passes.Load() == 0 {
		t.Fatal("successful health check did not end the panic episode")
	}
	panicking.panics.Store(true)
	checksBeforeSecondEpisode := panicking.checks.Load()
	deadline = time.Now().Add(time.Second)
	for panicking.checks.Load() == checksBeforeSecondEpisode && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if panicking.checks.Load() == checksBeforeSecondEpisode {
		t.Fatal("later panic did not start a new panic episode")
	}
	engine.Stop()

	out := logs.String()
	for _, want := range []string{"adapter_health_panic", "panic-watch", "test", "panic_type", "stack"} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q: %s", want, out)
		}
	}
	if strings.Contains(out, "secret-health-panic-value") {
		t.Fatalf("panic value leaked to log: %s", out)
	}
	if got := strings.Count(out, `"stack"`); got != 1 {
		t.Fatalf("stack log count across two quick panic episodes = %d, want 1: %s", got, out)
	}
}

func TestRecoveredAdapterRateLimitsFlappingHealthPanicStacks(t *testing.T) {
	var logs bytes.Buffer
	adapter := &healthPanicAdapter{}
	adapter.panics.Store(true)
	now := time.Date(2026, time.August, 12, 0, 0, 0, 0, time.UTC)
	wrapped := newRecoveredAdapter(adapter, AdapterConfig{Name: "flapping", Type: "test"}, slog.New(slog.NewJSONHandler(&logs, nil))).(*recoveredAdapter)
	wrapped.now = func() time.Time { return now }

	checkPanic := func() {
		t.Helper()
		if err := wrapped.HealthCheck(); err == nil {
			t.Fatal("HealthCheck unexpectedly succeeded")
		}
	}
	checkSuccess := func() {
		t.Helper()
		adapter.panics.Store(false)
		if err := wrapped.HealthCheck(); err != nil {
			t.Fatalf("HealthCheck: %v", err)
		}
		adapter.panics.Store(true)
	}

	checkPanic() // The first transition logs immediately.
	checkSuccess()
	checkPanic() // A quick flap is suppressed.
	if got := strings.Count(logs.String(), `"stack"`); got != 1 {
		t.Fatalf("stack log count inside rate window = %d, want 1", got)
	}

	now = now.Add(healthPanicStackLogInterval)
	checkSuccess()
	checkPanic()
	if got := strings.Count(logs.String(), `"stack"`); got != 2 {
		t.Fatalf("stack log count after rate window = %d, want 2", got)
	}
	if strings.Contains(logs.String(), "secret-health-panic-value") {
		t.Fatalf("panic value leaked to log: %s", logs.String())
	}
}

func wantStates(t *testing.T, states <-chan HealthState, want map[string]HealthStatus) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for len(want) > 0 {
		select {
		case state := <-states:
			if status, ok := want[state.WatcherName]; ok && state.Status == status {
				delete(want, state.WatcherName)
			}
		case <-timer.C:
			t.Fatalf("missing health states: %v", want)
		}
	}
}
