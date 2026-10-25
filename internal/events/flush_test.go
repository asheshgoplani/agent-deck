package events

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// flushPromptBound is far under the writer's one-second sync tick (which
// Flush used to wait for) and far over one append plus fsync.
const flushPromptBound = 500 * time.Millisecond

// Flush must not wait for the one-second sync tick: it asks the writer to
// append and fsync now. Durability is unchanged: when Flush returns true
// every published frame is written and synced to the active segment.
func TestFlushSyncsWithoutWaitingForTheTick(t *testing.T) {
	dir := t.TempDir()
	b, err := OpenAt(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for i := 1; i <= 3; i++ {
		b.Publish("probe", "s", map[string]any{"n": i})
		start := time.Now()
		if !b.Flush(2 * time.Second) {
			t.Fatalf("flush %d timed out", i)
		}
		if d := time.Since(start); d > flushPromptBound {
			t.Fatalf("flush %d took %v, want < %v (it waited for the sync tick)", i, d, flushPromptBound)
		}
		if w, s := b.written.Load(), b.synced.Load(); w != uint64(i) || s != uint64(i) {
			t.Fatalf("after flush %d: written=%d synced=%d", i, w, s)
		}
		if got := len(readLines(t, filepath.Join(dir, activeSegmentName))); got != i {
			t.Fatalf("after flush %d: %d lines on disk", i, got)
		}
		if b.Cursor() != Cursor(i) {
			t.Fatalf("after flush %d: cursor %d", i, b.Cursor())
		}
	}
}

// Flush racing Publish and Close stays race-free and never reports a frame
// durable that is not: a true Flush means synced covers what was enqueued.
// Concurrent flushers are each served promptly (40 durable flushes well
// inside the time two sync ticks would take), and Close lands mid-stream.
func TestFlushConcurrentWithPublishAndClose(t *testing.T) {
	b, err := OpenAt(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	const wantOK = 40
	var ok atomic.Int64
	reached := make(chan struct{})
	var once sync.Once
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				b.Publish("probe", "s", map[string]any{"i": i})
				target := b.enqueued.Load()
				if !b.Flush(2 * time.Second) {
					continue // closed under us
				}
				if s := b.synced.Load(); s < target {
					t.Errorf("flush true with synced=%d < enqueued=%d", s, target)
					return
				}
				if ok.Add(1) >= wantOK {
					once.Do(func() { close(reached) })
				}
			}
		}()
	}
	select {
	case <-reached:
	case <-time.After(1500 * time.Millisecond):
		t.Errorf("only %d of %d concurrent flushes completed in 1.5s", ok.Load(), wantOK)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if b.Flush(time.Second) {
		t.Fatal("flush on a closed bus reported success")
	}
}

// BenchmarkBusPublishFlush is one Publish followed by Flush, the shape of
// `agent-deck events publish`.
func BenchmarkBusPublishFlush(b *testing.B) {
	bus, err := OpenAt(b.TempDir(), Options{})
	if err != nil {
		b.Fatal(err)
	}
	defer bus.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bus.Publish("probe", "s", map[string]any{"i": i})
		if !bus.Flush(2 * time.Second) {
			b.Fatal("flush timed out")
		}
	}
}
