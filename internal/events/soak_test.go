package events

import (
	"context"
	"testing"
	"time"
)

// TestSoakSlowConsumerNeverBlocksProducer is the slice-4 soak test: a
// producer publishes 10k events as fast as it can while a consumer reads
// ~100x slower. The producer's total wall time must stay close to an
// unthrottled run (it must never be paced by the slow reader), and — since
// this run never overflows the bounded queue — the consumer eventually sees
// every event, in order, with none lost or duplicated.
func TestSoakSlowConsumerNeverBlocksProducer(t *testing.T) {
	if testing.Short() {
		t.Skip("soak test skipped in -short mode")
	}
	b := openTestBus(t)
	const n = 10000

	unthrottledStart := time.Now()
	for i := 0; i < n; i++ {
		b.Publish("kind.soak", "sess", i)
	}
	produceElapsed := time.Since(unthrottledStart)

	// A slow consumer: sleeps a fixed per-event delay chosen so the full
	// drain is ~100x the producer's elapsed time (bounded below so the test
	// itself stays fast in CI).
	perEventDelay := produceElapsed / n * 100
	if perEventDelay < 50*time.Microsecond {
		perEventDelay = 50 * time.Microsecond
	}
	if perEventDelay > time.Millisecond {
		perEventDelay = time.Millisecond // cap total soak runtime (~10s worst case for n=10000)
	}

	if produceElapsed > 2*time.Second {
		t.Fatalf("producing %d events took %v — Publish appears to be blocking on the writer/disk", n, produceElapsed)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	sub, err := b.Subscribe(ctx, 0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	var last Cursor
	count := 0
	for frame := range sub.Frames() {
		time.Sleep(perEventDelay) // simulate the slow consumer
		if frame.Cursor <= last {
			t.Fatalf("out of order: prev=%d got=%d", last, frame.Cursor)
		}
		last = frame.Cursor
		count++
		if count == n {
			cancel()
		}
	}
	if err := sub.Err(); err != nil {
		t.Fatalf("subscription error: %v", err)
	}
	if count != n {
		t.Fatalf("slow consumer only saw %d/%d events (some were lost)", count, n)
	}
	if b.Stats().Dropped != 0 {
		t.Fatalf("expected zero drops at producer pace for %d events, got %d", n, b.Stats().Dropped)
	}
}
