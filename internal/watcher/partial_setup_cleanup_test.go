package watcher

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
)

type partialSetupAdapter struct {
	setups    atomic.Int32
	teardowns atomic.Int32
}

func (a *partialSetupAdapter) Setup(context.Context, AdapterConfig) error {
	a.setups.Add(1)
	return errors.New("setup failed after allocation")
}

type retrySetupAdapter struct {
	setups    atomic.Int32
	teardowns atomic.Int32
}

func (a *retrySetupAdapter) Setup(context.Context, AdapterConfig) error {
	if a.setups.Add(1) == 1 {
		return errors.New("first setup failed after allocation")
	}
	return nil
}
func (*retrySetupAdapter) Listen(context.Context, chan<- Event) error { return nil }
func (a *retrySetupAdapter) Teardown() error {
	a.teardowns.Add(1)
	return nil
}
func (*retrySetupAdapter) HealthCheck() error { return nil }

func TestRecoveredAdapterRetryRetainsFinalCleanup(t *testing.T) {
	raw := &retrySetupAdapter{}
	engine := NewEngine(EngineConfig{Logger: slog.Default()})
	engine.RegisterAdapter("retry-id", raw, AdapterConfig{Name: "retry", Type: "test"}, 0)
	wrapped := engine.adapters[0].adapter

	if err := wrapped.Setup(context.Background(), AdapterConfig{}); err == nil {
		t.Fatal("first Setup unexpectedly succeeded")
	}
	if got := raw.teardowns.Load(); got != 1 {
		t.Fatalf("Teardown calls after failed Setup = %d, want 1", got)
	}
	if err := wrapped.Setup(context.Background(), AdapterConfig{}); err != nil {
		t.Fatalf("retry Setup: %v", err)
	}
	if err := wrapped.Teardown(); err != nil {
		t.Fatalf("final Teardown: %v", err)
	}
	if got := raw.teardowns.Load(); got != 2 {
		t.Fatalf("Teardown calls after successful retry = %d, want 2", got)
	}
}
func (*partialSetupAdapter) Listen(context.Context, chan<- Event) error { return nil }
func (a *partialSetupAdapter) Teardown() error {
	a.teardowns.Add(1)
	return nil
}
func (*partialSetupAdapter) HealthCheck() error { return nil }

func TestRecoveredAdapterCleansPartialSetupExactlyOnce(t *testing.T) {
	raw := &partialSetupAdapter{}
	engine, _ := newTestEngine(t, nil)
	engine.RegisterAdapter("partial-id", raw, AdapterConfig{Name: "partial", Type: "test"}, 0)

	if err := engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := raw.teardowns.Load(); got != 1 {
		t.Fatalf("Teardown calls after failed Setup = %d, want 1", got)
	}
	engine.Stop()
	if got := raw.teardowns.Load(); got != 1 {
		t.Fatalf("Teardown calls after Stop = %d, want 1", got)
	}
}
