package events

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Regression tests for #2484: Bus.Close must stop and wait for every
// subscription it started before it closes the lock and active files.

// runningSubscriptions counts goroutines currently inside
// (*Subscription).run, across every bus in the process.
func runningSubscriptions() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	return strings.Count(string(buf), "events.(*Subscription).run(")
}

// settledSubscriptions waits briefly for subscriptions left by earlier tests
// to finish exiting and returns the count that remains.
func settledSubscriptions() int {
	n := runningSubscriptions()
	for i := 0; i < 50; i++ {
		time.Sleep(10 * time.Millisecond)
		m := runningSubscriptions()
		if m == n {
			return n
		}
		n = m
	}
	return n
}

// closeWithin calls b.Close and fails the test if it does not return within d.
func closeWithin(t *testing.T, b *Bus, d time.Duration) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- b.Close() }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		t.Fatalf("Close did not return within %v", d)
		return nil
	}
}

// waitClosed fails unless sub's channel closes within d (frames still
// buffered are drained) and returns how many frames were drained.
func waitClosed(t *testing.T, sub *Subscription, d time.Duration) int {
	t.Helper()
	deadline := time.After(d)
	n := 0
	for {
		select {
		case _, ok := <-sub.Frames():
			if !ok {
				return n
			}
			n++
		case <-deadline:
			t.Fatalf("subscription channel still open %v after Close", d)
			return n
		}
	}
}

type busMode struct {
	name string
	opts Options
}

var closeModes = []busMode{
	{"plain", Options{}},
	{"keepCorrupt", Options{KeepCorrupt: true, Private: true}},
}

func TestCloseStopsLivePollingSubscriptions(t *testing.T) {
	for _, m := range closeModes {
		t.Run(m.name, func(t *testing.T) {
			baseline := settledSubscriptions()
			b, err := OpenAt(t.TempDir(), m.opts)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 5; i++ {
				if _, err := b.Commit("turn", "s", map[string]any{"n": i}); err != nil {
					t.Fatal(err)
				}
			}
			const nsubs = 6
			subs := make([]*Subscription, nsubs)
			var wg sync.WaitGroup
			for i := range subs {
				sub, err := b.Subscribe(context.Background(), 0)
				if err != nil {
					t.Fatal(err)
				}
				subs[i] = sub
				// A consumer that reads everything, so each subscription
				// catches up and polls the active segment (streamActive, or
				// streamActiveCommitted under keepCorrupt).
				wg.Add(1)
				go func(s *Subscription) {
					defer wg.Done()
					for range s.Frames() {
					}
				}(sub)
			}
			// Write while the subscriptions poll, so each one is mid-poll on a
			// growing active segment.
			stop := make(chan struct{})
			var writer sync.WaitGroup
			writer.Add(1)
			go func() {
				defer writer.Done()
				for i := 0; ; i++ {
					select {
					case <-stop:
						return
					default:
					}
					if _, err := b.Commit("turn", "s", map[string]any{"n": i}); err != nil {
						return // closed
					}
					time.Sleep(time.Millisecond)
				}
			}()
			time.Sleep(60 * time.Millisecond)
			// Stop the writer before Close: a Commit racing Close is a separate
			// path, outside #2484. The subscriptions keep polling the active
			// segment, so Close still lands mid-poll.
			close(stop)
			writer.Wait()
			if err := closeWithin(t, b, 5*time.Second); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if n := runningSubscriptions(); n > baseline {
				t.Fatalf("%d subscription goroutines still running after Close returned (baseline %d)", n, baseline)
			}
			// Give a still-running poller time to touch the closed lock file.
			time.Sleep(3 * pollInterval)
			waitDone := make(chan struct{})
			go func() { wg.Wait(); close(waitDone) }()
			select {
			case <-waitDone:
			case <-time.After(5 * time.Second):
				t.Fatal("a subscription channel never closed after Close")
			}
			for i, s := range subs {
				if err := s.Err(); err != nil {
					t.Fatalf("subscription %d ended with %v; Close must end it cleanly", i, err)
				}
			}
		})
	}
}

func TestCloseStopsReadOnlyFollowerSubscriptions(t *testing.T) {
	for _, m := range closeModes {
		t.Run(m.name, func(t *testing.T) {
			dir := t.TempDir()
			w, err := OpenAt(dir, m.opts)
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			for i := 0; i < 3; i++ {
				if _, err := w.Commit("turn", "s", map[string]any{"n": i}); err != nil {
					t.Fatal(err)
				}
			}
			ro := m.opts
			ro.ReadOnly = true
			r, err := OpenAt(dir, ro)
			if err != nil {
				t.Fatal(err)
			}
			baseline := settledSubscriptions()
			subs := make([]*Subscription, 4)
			for i := range subs {
				subs[i], _ = r.Subscribe(context.Background(), 0)
			}
			for _, s := range subs {
				for j := 0; j < 3; j++ {
					select {
					case <-s.Frames():
					case <-time.After(5 * time.Second):
						t.Fatal("follower never caught up")
					}
				}
			}
			time.Sleep(3 * pollInterval) // let them poll the active segment
			if err := closeWithin(t, r, 5*time.Second); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if n := runningSubscriptions(); n > baseline {
				t.Fatalf("%d subscription goroutines still running after Close returned (baseline %d)", n, baseline)
			}
			time.Sleep(3 * pollInterval)
			for i, s := range subs {
				waitClosed(t, s, 5*time.Second)
				if err := s.Err(); err != nil {
					t.Fatalf("follower %d ended with %v", i, err)
				}
			}
		})
	}
}

func TestCloseDoesNotHangOnAConsumerThatNeverReads(t *testing.T) {
	for _, m := range closeModes {
		t.Run(m.name, func(t *testing.T) {
			baseline := settledSubscriptions()
			b, err := OpenAt(t.TempDir(), m.opts)
			if err != nil {
				t.Fatal(err)
			}
			// More frames than the channel buffers, so the send blocks.
			for i := 0; i < 200; i++ {
				if _, err := b.Commit("turn", "s", map[string]any{"n": i}); err != nil {
					t.Fatal(err)
				}
			}
			sub, _ := b.Subscribe(context.Background(), 0)
			deadline := time.Now().Add(5 * time.Second)
			for len(sub.Frames()) < cap(sub.Frames()) {
				if time.Now().After(deadline) {
					t.Fatal("subscription never filled its channel")
				}
				time.Sleep(time.Millisecond)
			}
			if err := closeWithin(t, b, 5*time.Second); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if n := runningSubscriptions(); n > baseline {
				t.Fatalf("blocked subscription still running after Close returned (%d > baseline %d)", n, baseline)
			}
			if n := waitClosed(t, sub, 5*time.Second); n > cap(sub.Frames()) {
				t.Fatalf("drained %d frames after Close; the blocked send should have been abandoned", n)
			}
		})
	}
}

func TestSubscribeAfterCloseReturnsAClosedSubscription(t *testing.T) {
	for _, m := range closeModes {
		t.Run(m.name, func(t *testing.T) {
			b, err := OpenAt(t.TempDir(), m.opts)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := b.Commit("turn", "s", map[string]any{"n": 1}); err != nil {
				t.Fatal(err)
			}
			if err := b.Close(); err != nil {
				t.Fatal(err)
			}
			baseline := settledSubscriptions()
			sub, err := b.Subscribe(context.Background(), 0)
			if err != nil {
				t.Fatalf("Subscribe after Close: %v", err)
			}
			if sub == nil {
				t.Fatal("Subscribe after Close returned nil")
			}
			if n := runningSubscriptions(); n > baseline {
				t.Fatalf("Subscribe after Close started a goroutine (%d > baseline %d)", n, baseline)
			}
			if n := waitClosed(t, sub, time.Second); n != 0 {
				t.Fatalf("closed bus streamed %d frames", n)
			}
			if err := sub.Err(); err != nil {
				t.Fatalf("Subscribe after Close ended with %v", err)
			}
		})
	}
}

func TestCloseIsIdempotentAndConcurrentSafe(t *testing.T) {
	for _, m := range closeModes {
		t.Run(m.name, func(t *testing.T) {
			b, err := OpenAt(t.TempDir(), m.opts)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 10; i++ {
				b.Publish("status", "s", map[string]any{"n": i})
				if _, err := b.Commit("turn", "s", map[string]any{"n": i}); err != nil {
					t.Fatal(err)
				}
			}
			subs := make([]*Subscription, 4)
			for i := range subs {
				subs[i], _ = b.Subscribe(context.Background(), 0)
			}
			time.Sleep(3 * pollInterval)
			var wg sync.WaitGroup
			errs := make(chan error, 8)
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					errs <- b.Close()
				}()
			}
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("concurrent Close calls did not return")
			}
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatalf("Close: %v", err)
				}
			}
			if err := closeWithin(t, b, time.Second); err != nil {
				t.Fatalf("second Close: %v", err)
			}
			for _, s := range subs {
				waitClosed(t, s, 5*time.Second)
				if err := s.Err(); err != nil {
					t.Fatalf("subscription ended with %v", err)
				}
			}
		})
	}
}

// A consumer may close the bus from inside its range over Frames (an owner
// that stops at the first frame it wants). Close must not wait on the very
// subscription whose consumer is calling it.
func TestCloseFromInsideASubscriptionConsumer(t *testing.T) {
	for _, m := range closeModes {
		t.Run(m.name, func(t *testing.T) {
			b, err := OpenAt(t.TempDir(), m.opts)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 200; i++ {
				if _, err := b.Commit("turn", "s", map[string]any{"n": i}); err != nil {
					t.Fatal(err)
				}
			}
			sub, _ := b.Subscribe(context.Background(), 0)
			done := make(chan error, 1)
			go func() {
				for range sub.Frames() {
					done <- b.Close()
					return
				}
				done <- nil
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("Close: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Close from inside the consumer deadlocked")
			}
			waitClosed(t, sub, 5*time.Second)
		})
	}
}

// After Close returns, no subscription goroutine may touch the bus: the
// active-segment hook (called on every poll) must never run again.
func TestNoSubscriptionTouchesTheBusAfterClose(t *testing.T) {
	for _, m := range closeModes {
		t.Run(m.name, func(t *testing.T) {
			b, err := OpenAt(t.TempDir(), m.opts)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := b.Commit("turn", "s", map[string]any{"n": 1}); err != nil {
				t.Fatal(err)
			}
			baseline := settledSubscriptions()
			var mu sync.Mutex
			closed := false
			late := 0
			hook := func() {
				mu.Lock()
				if closed {
					late++
				}
				mu.Unlock()
			}
			subscribeActiveHook.Store(&hook)
			defer subscribeActiveHook.Store(nil)
			subs := make([]*Subscription, 4)
			for i := range subs {
				subs[i], _ = b.Subscribe(context.Background(), 0)
				go func(s *Subscription) {
					for range s.Frames() {
					}
				}(subs[i])
			}
			time.Sleep(3 * pollInterval)
			if err := closeWithin(t, b, 5*time.Second); err != nil {
				t.Fatalf("Close: %v", err)
			}
			mu.Lock()
			closed = true
			mu.Unlock()
			if n := runningSubscriptions(); n > baseline {
				t.Fatalf("%d subscription goroutines still running after Close returned (baseline %d)", n, baseline)
			}
			time.Sleep(5 * pollInterval)
			mu.Lock()
			n := late
			mu.Unlock()
			if n > 0 {
				t.Fatalf("subscriptions polled the bus %d times after Close returned", n)
			}
			for i, s := range subs {
				waitClosed(t, s, 5*time.Second)
				if err := s.Err(); err != nil {
					t.Fatalf("subscription %d ended with %v", i, err)
				}
			}
		})
	}
}
