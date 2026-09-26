package events

import (
	"context"
	"fmt"
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	sub, err := b.Subscribe(ctx, 0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	type result struct {
		count int
		err   error
	}
	readDone := make(chan result, 1)
	go func() {
		var last Cursor
		count := 0
		for frame := range sub.Frames() {
			time.Sleep(time.Millisecond) // concurrent consumer, over 100x slower than Publish
			if frame.Cursor <= last {
				readDone <- result{count, fmt.Errorf("out of order: prev=%d got=%d", last, frame.Cursor)}
				cancel()
				return
			}
			last = frame.Cursor
			count++
			if count == n {
				cancel()
			}
		}
		readDone <- result{count, sub.Err()}
	}()
	unthrottledStart := time.Now()
	for i := 0; i < n; i++ {
		b.Publish("kind.soak", "sess", i)
	}
	produceElapsed := time.Since(unthrottledStart)
	if produceElapsed > 2*time.Second {
		t.Fatalf("producing %d events took %v: Publish blocked", n, produceElapsed)
	}
	read := <-readDone
	if read.err != nil {
		t.Fatal(read.err)
	}
	if err := sub.Err(); err != nil {
		t.Fatalf("subscription error: %v", err)
	}
	if read.count != n {
		t.Fatalf("slow consumer only saw %d/%d events (some were lost)", read.count, n)
	}
	if b.Stats().Dropped != 0 {
		t.Fatalf("expected zero drops at producer pace for %d events, got %d", n, b.Stats().Dropped)
	}
}
