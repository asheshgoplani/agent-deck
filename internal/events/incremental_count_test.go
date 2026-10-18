package events

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// lockedFrames reads a Bus's activeFrames under its lock.
func lockedFrames(b *Bus) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.activeFrames
}

// #2770: two writers on one profile (TUI and notify daemon). After the other
// writer appends one frame, this writer's one-second sync tick must count
// only the appended bytes, not re-read the whole multi-megabyte active
// segment, and still know the true line count.
func TestSyncTickAfterForeignAppendReadsOnlyNewBytes(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for _, bus := range []*Bus{a, b} {
		bus.maxSegBytes, bus.maxSegFrames = 1<<40, 1<<30
	}
	active := filepath.Join(dir, activeSegmentName)

	a.writeBatch(reviewFrames(20000, "a"), true)
	b.writeBatch(reviewFrames(1, "b"), true) // B's first look may count it all
	a.writeBatch(nil, true)

	info, err := os.Stat(active)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() < 1<<20 {
		t.Fatalf("active segment only %d bytes; test needs a large one", info.Size())
	}

	b.writeBatch(reviewFrames(1, "b"), true)
	before := countedBytes.Load()
	a.writeBatch(nil, true) // A's sync tick after B's append
	read := countedBytes.Load() - before
	t.Logf("sync tick counted %d bytes of a %d-byte active file", read, info.Size())
	if read > 64*1024 {
		t.Errorf("sync tick read %d bytes to count lines of a %d-byte active file; want only the appended range", read, info.Size())
	}
	if got, want := lockedFrames(a), countLines(dir, activeSegmentName); got != want || want != 20002 {
		t.Errorf("A activeFrames = %d, file has %d lines (want 20002)", got, want)
	}

	// Rotation by the other writer: A must recount the new active file.
	b.mu.Lock()
	b.maxSegFrames = 1
	b.mu.Unlock()
	b.writeBatch(reviewFrames(1, "b"), true) // rotates
	b.mu.Lock()
	b.maxSegFrames = 1 << 30
	b.mu.Unlock()
	b.writeBatch(reviewFrames(2, "b"), true)
	a.writeBatch(nil, true)
	if got, want := lockedFrames(a), countLines(dir, activeSegmentName); got != want || want != 2 {
		t.Errorf("after rotation A activeFrames = %d, file has %d lines (want 2)", got, want)
	}
	a.writeBatch(reviewFrames(50, "a"), true)

	// Shrink: the active file is cut back to whole lines, then B appends.
	data, err := os.ReadFile(active)
	if err != nil {
		t.Fatal(err)
	}
	cut := 0
	for i := 0; i < 10; i++ {
		cut += bytes.IndexByte(data[cut:], '\n') + 1
	}
	if err := os.Truncate(active, int64(cut)); err != nil {
		t.Fatal(err)
	}
	b.writeBatch(reviewFrames(1, "b"), true)
	a.writeBatch(nil, true)
	if got, want := lockedFrames(a), countLines(dir, activeSegmentName); got != want || want != 11 {
		t.Errorf("after shrink A activeFrames = %d, file has %d lines (want 11)", got, want)
	}
	reviewCheckDir(t, dir)
}
