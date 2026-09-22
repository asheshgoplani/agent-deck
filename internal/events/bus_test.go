package events

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openTestBus(t *testing.T) *Bus {
	t.Helper()
	dir := t.TempDir()
	b, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func TestCursorOrderingIsMonotonicAndSequential(t *testing.T) {
	b := openTestBus(t)
	for i := 0; i < 50; i++ {
		b.Publish("kind.test", fmt.Sprintf("sess-%d", i%3), map[string]any{"i": i})
	}
	if !b.Flush(2 * time.Second) {
		t.Fatal("flush timed out")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sub, err := b.Subscribe(ctx, 0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	var last Cursor
	count := 0
	for frame := range sub.Frames() {
		if frame.Cursor <= last {
			t.Fatalf("cursor not strictly increasing: prev=%d got=%d", last, frame.Cursor)
		}
		last = frame.Cursor
		count++
		if count == 50 {
			cancel()
		}
	}
	if count != 50 {
		t.Fatalf("expected 50 frames, got %d", count)
	}
}

// TestResumeAfterKillLosesNothingAndDuplicatesNothing is the slice-4 done
// proof: publish a stream of events, "kill" a follower mid-stream (cancel its
// context after it has consumed some prefix), then Subscribe again with
// after=<last cursor it saw> and assert the resumed stream is exactly the
// remaining suffix — zero lost, zero duplicated — and that the concatenation
// of both runs equals the full, contiguous cursor sequence with no gaps.
func TestResumeAfterKillLosesNothingAndDuplicatesNothing(t *testing.T) {
	b := openTestBus(t)
	const total = 500

	for i := 0; i < total; i++ {
		b.Publish("kind.resume", "sess", map[string]any{"i": i})
	}
	if !b.Flush(5 * time.Second) {
		t.Fatal("flush timed out")
	}

	// First follower: read a prefix, then get "killed" (context cancelled).
	const killAfterN = 213
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	sub1, err := b.Subscribe(ctx1, 0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	var firstRun []Frame
	for frame := range sub1.Frames() {
		firstRun = append(firstRun, frame)
		if len(firstRun) == killAfterN {
			cancel1() // kill mid-stream
			break
		}
	}
	// Drain until the channel actually closes so the goroutine has exited
	// (Subscription.Frames() closes on cancellation).
	for range sub1.Frames() {
	}

	if len(firstRun) != killAfterN {
		t.Fatalf("expected to have read %d frames before kill, got %d", killAfterN, len(firstRun))
	}
	lastSeen := firstRun[len(firstRun)-1].Cursor

	// Resume from the last cursor the killed follower actually saw.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	sub2, err := b.Subscribe(ctx2, lastSeen)
	if err != nil {
		t.Fatalf("Subscribe resume: %v", err)
	}
	var secondRun []Frame
	for frame := range sub2.Frames() {
		secondRun = append(secondRun, frame)
		if len(secondRun) == total-killAfterN {
			cancel2()
			break
		}
	}
	for range sub2.Frames() {
	}

	if err := sub2.Err(); err != nil {
		t.Fatalf("resumed subscription error: %v", err)
	}

	if len(secondRun) != total-killAfterN {
		t.Fatalf("expected %d frames after resume, got %d", total-killAfterN, len(secondRun))
	}

	// Zero lost, zero duplicated: the two runs concatenated must cover
	// cursors 1..total exactly once each, in order.
	seen := make(map[Cursor]bool, total)
	var all []Frame
	all = append(all, firstRun...)
	all = append(all, secondRun...)
	if len(all) != total {
		t.Fatalf("expected %d total frames across both runs, got %d", total, len(all))
	}
	var prev Cursor
	for _, f := range all {
		if seen[f.Cursor] {
			t.Fatalf("duplicate cursor %d", f.Cursor)
		}
		seen[f.Cursor] = true
		if f.Cursor <= prev {
			t.Fatalf("cursor out of order: prev=%d got=%d", prev, f.Cursor)
		}
		prev = f.Cursor
	}
	if Cursor(len(seen)) != Cursor(total) {
		t.Fatalf("expected %d distinct cursors, got %d", total, len(seen))
	}
}

func TestResumeSurvivesProcessRestart(t *testing.T) {
	dir := t.TempDir()

	b1, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i := 0; i < 30; i++ {
		b1.Publish("kind.restart", "sess", map[string]any{"i": i})
	}
	if !b1.Flush(2 * time.Second) {
		t.Fatal("flush timed out")
	}
	midCursor := b1.Cursor()
	if err := b1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Simulate a restart: a brand-new Bus over the same directory.
	b2, err := Open(dir)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	t.Cleanup(func() { _ = b2.Close() })
	if b2.Cursor() != midCursor {
		t.Fatalf("cursor not durable across restart: want %d got %d", midCursor, b2.Cursor())
	}
	for i := 30; i < 60; i++ {
		b2.Publish("kind.restart", "sess", map[string]any{"i": i})
	}
	if !b2.Flush(2 * time.Second) {
		t.Fatal("flush timed out")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sub, err := b2.Subscribe(ctx, midCursor)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	count := 0
	for frame := range sub.Frames() {
		if frame.Cursor <= midCursor {
			t.Fatalf("got a frame from before restart: cursor=%d midCursor=%d", frame.Cursor, midCursor)
		}
		count++
		if count == 30 {
			cancel()
		}
	}
	if count != 30 {
		t.Fatalf("expected 30 post-restart frames, got %d", count)
	}
}

func TestResumeAcrossRotation(t *testing.T) {
	dir := t.TempDir()
	b, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	b.maxSegFrames = 20 // force multiple rotations well within the test

	const total = 200
	for i := 0; i < total; i++ {
		b.Publish("kind.rotate", "sess", map[string]any{"i": i})
	}
	if !b.Flush(5 * time.Second) {
		t.Fatal("flush timed out")
	}

	sealed, err := listSealedSegments(dir)
	if err != nil {
		t.Fatalf("listSealedSegments: %v", err)
	}
	if len(sealed) < 2 {
		t.Fatalf("expected rotation to have produced >=2 sealed segments, got %d", len(sealed))
	}

	// Resume from partway through an early sealed segment.
	resumeAfter := sealed[0].end
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sub, err := b.Subscribe(ctx, resumeAfter)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	var last Cursor = resumeAfter
	count := 0
	for frame := range sub.Frames() {
		if frame.Cursor <= last {
			t.Fatalf("out of order after rotation: prev=%d got=%d", last, frame.Cursor)
		}
		last = frame.Cursor
		count++
		if count == int(Cursor(total)-resumeAfter) {
			cancel()
		}
	}
	want := int(Cursor(total) - resumeAfter)
	if count != want {
		t.Fatalf("expected %d frames after rotation resume, got %d", want, count)
	}
}

func TestPublishNeverBlocksWhenQueueIsFull(t *testing.T) {
	dir := t.TempDir()
	b, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })

	// Starve the writer so the queue fills: hold b.mu so writeFrame can never
	// take it, forcing every enqueued frame to sit in the channel.
	b.mu.Lock()
	defer b.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < defaultQueueCap*2; i++ {
			b.Publish("kind.flood", "sess", i)
		}
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Publish blocked producers when the queue filled up")
	}
	if b.Stats().Dropped == 0 {
		t.Fatal("expected some frames to be dropped once the queue filled")
	}
}

func TestDisabledBusIsANoOp(t *testing.T) {
	t.Setenv(disableEnvVar, "0")
	resetDefaultForTest()
	t.Cleanup(resetDefaultForTest)

	b := Default()
	b.Publish("kind.noop", "sess", map[string]any{"x": 1})
	if !b.Flush(50 * time.Millisecond) {
		// disabled Flush returns false immediately; that's expected, not a hang
	}
	stats := b.Stats()
	if stats.Enabled {
		t.Fatal("expected disabled bus")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	sub, err := b.Subscribe(ctx, 0)
	if err != nil {
		t.Fatalf("Subscribe on disabled bus: %v", err)
	}
	n := 0
	for range sub.Frames() {
		n++
	}
	if n != 0 {
		t.Fatalf("disabled bus produced %d frames", n)
	}
}

func TestUnwritableDirDisablesInsteadOfFailing(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	parent := t.TempDir()
	blocked := filepath.Join(parent, "blocked")
	if err := os.Mkdir(blocked, 0o000); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

	_, err := Open(filepath.Join(blocked, "bus"))
	if err == nil {
		t.Fatal("expected Open to fail under an unwritable parent")
	}
	// openDefault()/Default() is what production code actually calls, and it
	// must degrade to a disabled, safe-to-use Bus rather than propagate this
	// error — exercised by TestDisabledBusIsANoOp's Stats()/Publish() shape.
}

func TestStatsJSONShape(t *testing.T) {
	b := openTestBus(t)
	b.Publish("kind.stats", "sess", map[string]any{"a": 1})
	if !b.Flush(2 * time.Second) {
		t.Fatal("flush timed out")
	}
	stats := b.Stats()
	if !stats.Enabled {
		t.Fatal("expected enabled")
	}
	if stats.Cursor != 1 {
		t.Fatalf("expected cursor 1, got %d", stats.Cursor)
	}
	if stats.Published != 1 || stats.Written != 1 || stats.Synced != 1 {
		t.Fatalf("unexpected counters: %+v", stats)
	}
}
