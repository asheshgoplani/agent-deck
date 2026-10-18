package web

import (
	"context"
	"testing"
	"time"
)

// blockingFinalizer is a mutator whose undoable-delete sweep hangs (an inbox
// file under lock contention).
type blockingFinalizer struct {
	*fakeMutator
	release chan struct{}
}

func (b *blockingFinalizer) FinalizePendingDeletes() { <-b.release }

// Shutdown waits for the undoable-delete sweep at most its cap.
func TestShutdownBoundsPendingDeleteFinalize(t *testing.T) {
	prev := pendingDeleteFinalizeTimeout
	pendingDeleteFinalizeTimeout = 50 * time.Millisecond
	t.Cleanup(func() { pendingDeleteFinalizeTimeout = prev })
	f := &blockingFinalizer{fakeMutator: &fakeMutator{}, release: make(chan struct{})}
	t.Cleanup(func() { close(f.release) })

	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.SetMutator(f)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { _ = srv.Shutdown(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown waited past 2s for a hung undoable-delete sweep; want at most its cap")
	}
}
