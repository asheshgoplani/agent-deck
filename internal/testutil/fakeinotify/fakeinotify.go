// Package fakeinotify provides a controllable filesystem event source for
// tests that exercise hook-status-watcher overflow / fallback paths
// (TEST-PLAN.md J2 regression, TUI-TEST-PLAN.md §6.2 fakeInotify).
//
// The harness exposes an EventSource interface mirroring the subset of
// *fsnotify.Watcher production code consumes (Events / Errors / Close).
// Production refactor — injecting the source into StatusFileWatcher — is
// a prerequisite for Phase 1 watcher tests but lives outside this PR.
//
// Usage:
//
//	f := fakeinotify.New(t)
//	defer f.Close()
//	f.Publish("/h/abc.json", fsnotify.Create)
//	f.SimulateOverflow(100) // kernel queue overflow
//	// Wire f as the EventSource for StatusFileWatcher under test.
package fakeinotify

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/fsnotify/fsnotify"
)

// EventSource is the seam that production code (e.g. StatusFileWatcher)
// should depend on instead of *fsnotify.Watcher. Both Fake and a thin
// adapter around *fsnotify.Watcher satisfy it.
type EventSource interface {
	Events() <-chan fsnotify.Event
	Errors() <-chan error
	Close() error
}

// Fake is a controllable EventSource. Concurrent Publish / DropAfter /
// Close are safe.
type Fake struct {
	t *testing.T

	events chan fsnotify.Event
	errors chan error

	mu      sync.Mutex
	limit   int   // -1 = unlimited; otherwise max events to deliver before dropping
	dropped int64 // atomic counter so Dropped() doesn't need the mutex
	closed  bool
}

// New returns a fresh Fake with reasonable channel buffers (256 events,
// 16 errors). The test's t handle is retained for cleanup registration.
func New(t *testing.T) *Fake {
	t.Helper()
	f := &Fake{
		t:      t,
		events: make(chan fsnotify.Event, 256),
		errors: make(chan error, 16),
		limit:  -1,
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// Events returns the channel a watcher under test should read from.
func (f *Fake) Events() <-chan fsnotify.Event { return f.events }

// Errors returns the channel a watcher under test should read errors from.
func (f *Fake) Errors() <-chan error { return f.errors }

// Publish delivers a synthetic event. Events past DropAfter's threshold
// are silently dropped and counted (see Dropped).
func (f *Fake) Publish(name string, op fsnotify.Op) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	if f.limit == 0 {
		f.mu.Unlock()
		atomic.AddInt64(&f.dropped, 1)
		return
	}
	if f.limit > 0 {
		f.limit--
	}
	f.mu.Unlock()

	select {
	case f.events <- fsnotify.Event{Name: name, Op: op}:
	default:
		// Channel buffer exceeded — count as dropped (mirrors a real
		// inotify queue that the consumer hasn't drained).
		atomic.AddInt64(&f.dropped, 1)
	}
}

// PublishError delivers a synthetic error to the Errors channel.
func (f *Fake) PublishError(err error) {
	f.mu.Lock()
	closed := f.closed
	f.mu.Unlock()
	if closed {
		return
	}
	select {
	case f.errors <- err:
	default:
	}
}

// DropAfter sets the maximum number of subsequent Publish calls that will
// reach the Events channel; further Publish calls become drops. Pass -1
// to disable the cap. Pass 0 to drop everything from now on.
func (f *Fake) DropAfter(n int) {
	f.mu.Lock()
	f.limit = n
	f.mu.Unlock()
}

// Dropped returns the cumulative number of events that were not
// delivered (either because of DropAfter or buffer exhaustion).
func (f *Fake) Dropped() int { return int(atomic.LoadInt64(&f.dropped)) }

// SimulateOverflow models the kernel inotify-queue overflow scenario:
// it counts n events as dropped and emits a single
// fsnotify.ErrEventOverflow on the Errors channel — matching what
// fsnotify itself surfaces when the kernel reports IN_Q_OVERFLOW.
//
// Tests assert that the watcher activates its disk-poll fallback after
// observing this signal (J2 regression).
func (f *Fake) SimulateOverflow(n int) {
	if n > 0 {
		atomic.AddInt64(&f.dropped, int64(n))
	}
	f.PublishError(fsnotify.ErrEventOverflow)
}

// Close shuts down both channels. Idempotent.
func (f *Fake) Close() error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil
	}
	f.closed = true
	close(f.events)
	close(f.errors)
	f.mu.Unlock()
	return nil
}
