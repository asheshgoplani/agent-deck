package events

import (
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// Pending-delivery retention (comms ledger P2): a segment a reader still
// needs survives both the age and the count bound, and is compacted as soon
// as the reader moves past it.
func TestRetainFromKeepsSegmentsAReaderStillNeeds(t *testing.T) {
	dir := t.TempDir()
	var held atomic.Uint64
	b, err := OpenAt(dir, Options{MaxSegmentBytes: 1, RetentionDays: 1, RetainSegments: 1,
		RetainFrom: func() Cursor { return Cursor(held.Load()) }})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	held.Store(2) // a reader has not acknowledged cursor 2 yet
	for i := 0; i < 5; i++ {
		if _, err := b.Commit("k", "s", nil); err != nil {
			t.Fatal(err)
		}
	}
	sealed, err := listSealedSegments(dir)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, s := range sealed {
		_ = os.Chtimes(s.path, old, old)
	}
	if _, err := b.Commit("k", "s", nil); err != nil {
		t.Fatal(err)
	}
	oldest, err := b.Oldest()
	if err != nil {
		t.Fatal(err)
	}
	if oldest != 2 {
		t.Fatalf("held cursor 2: oldest retained %d, want 2 (segment 1 dropped, 2 kept against age and count)", oldest)
	}
	held.Store(0) // the reader caught up
	if _, err := b.Commit("k", "s", nil); err != nil {
		t.Fatal(err)
	}
	if oldest, _ = b.Oldest(); oldest <= 2 {
		t.Fatalf("released segments must compact again: oldest %d", oldest)
	}
}

func TestCursorBeforeSkipsSegmentsOlderThanTheWindow(t *testing.T) {
	dir := t.TempDir()
	b, err := OpenAt(dir, Options{MaxSegmentBytes: 1, RetainSegments: 100})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for i := 0; i < 4; i++ {
		if _, err := b.Commit("k", "s", nil); err != nil {
			t.Fatal(err)
		}
	}
	sealed, _ := listSealedSegments(dir)
	old := time.Now().Add(-72 * time.Hour)
	for _, s := range sealed[:2] {
		_ = os.Chtimes(s.path, old, old)
	}
	got, err := b.CursorBefore(time.Now().Add(-24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got != sealed[2].start-1 {
		t.Fatalf("CursorBefore = %d, want %d (just before the first segment inside the window)", got, sealed[2].start-1)
	}
	if got, _ := b.CursorBefore(time.Now().Add(-100 * time.Hour)); got != 0 {
		t.Fatalf("a window older than everything reads from the start: %d", got)
	}
}
