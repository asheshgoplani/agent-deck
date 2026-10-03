package events

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestCommitIsDurableOrderedAndReturnsItsCursor(t *testing.T) {
	dir := t.TempDir()
	b, err := OpenAt(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for i := 1; i <= 3; i++ {
		f, err := b.Commit("turn", "child", map[string]any{"n": i})
		if err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
		if f.Cursor != Cursor(i) {
			t.Fatalf("commit %d got cursor %d", i, f.Cursor)
		}
		if f.EventID == "" || f.Kind != "turn" || f.SessionID != "child" {
			t.Fatalf("commit %d frame %+v", i, f)
		}
		// Durable before Commit returns: the line is on disk now, not after a flush tick.
		lines := readLines(t, filepath.Join(dir, activeSegmentName))
		if len(lines) != i {
			t.Fatalf("after commit %d: %d lines on disk", i, len(lines))
		}
	}
	if b.synced.Load() != 3 || b.written.Load() != 3 {
		t.Fatalf("synced=%d written=%d", b.synced.Load(), b.written.Load())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sub, _ := b.Subscribe(ctx, 0)
	var got []Cursor
	for f := range sub.Frames() {
		got = append(got, f.Cursor)
		if len(got) == 3 {
			cancel()
		}
	}
	if len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Fatalf("subscribe saw %v", got)
	}
}

func TestCommitAndPublishShareOneCursorSequence(t *testing.T) {
	dir := t.TempDir()
	b, err := OpenAt(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	b.Publish("a", "s", nil)
	if !b.Flush(2 * time.Second) {
		t.Fatal("flush")
	}
	f, err := b.Commit("b", "s", nil)
	if err != nil {
		t.Fatal(err)
	}
	b.Publish("c", "s", nil)
	if !b.Flush(2 * time.Second) {
		t.Fatal("flush")
	}
	if f.Cursor != 2 || b.Cursor() != 3 {
		t.Fatalf("commit cursor %d, bus cursor %d", f.Cursor, b.Cursor())
	}
	seen := map[Cursor]bool{}
	for _, line := range readLines(t, filepath.Join(dir, activeSegmentName)) {
		fr, err := ParseFrameLine([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		if seen[fr.Cursor] {
			t.Fatalf("duplicate cursor %d", fr.Cursor)
		}
		seen[fr.Cursor] = true
	}
	if len(seen) != 3 {
		t.Fatalf("%d frames on disk", len(seen))
	}
}

func TestTwoWritersOnOneLogNeverShareACursor(t *testing.T) {
	dir := t.TempDir()
	w1, err := OpenAt(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w1.Close()
	w2, err := OpenAt(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()
	var cursors []Cursor
	for i := 0; i < 10; i++ {
		w := w1
		if i%2 == 1 {
			w = w2
		}
		f, err := w.Commit("k", "s", nil)
		if err != nil {
			t.Fatal(err)
		}
		cursors = append(cursors, f.Cursor)
	}
	for i, c := range cursors {
		if c != Cursor(i+1) {
			t.Fatalf("cursor sequence %v", cursors)
		}
	}
}

func TestCommitRotatesAndRetentionDaysDropsOldSegments(t *testing.T) {
	dir := t.TempDir()
	b, err := OpenAt(dir, Options{MaxSegmentBytes: 1, RetentionDays: 1, RetainSegments: 100})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for i := 0; i < 3; i++ {
		if _, err := b.Commit("k", "s", nil); err != nil {
			t.Fatal(err)
		}
	}
	sealed, err := listSealedSegments(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sealed) != 3 {
		t.Fatalf("expected one sealed segment per commit at a 1-byte cap, got %d", len(sealed))
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(sealed[0].path, old, old); err != nil {
		t.Fatal(err)
	}
	// The next rotation compacts: the aged segment goes, the fresh ones stay.
	if _, err := b.Commit("k", "s", nil); err != nil {
		t.Fatal(err)
	}
	after, err := listSealedSegments(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range after {
		if s.path == sealed[0].path {
			t.Fatalf("aged segment %s survived compaction", s.path)
		}
	}
	if len(after) != 3 {
		t.Fatalf("expected 3 fresh sealed segments, got %d", len(after))
	}
	if b.Cursor() != 4 {
		t.Fatalf("cursor %d", b.Cursor())
	}
}

func TestReadOnlyBusFollowsButNeverWrites(t *testing.T) {
	dir := t.TempDir()
	if _, err := OpenAt(dir+"/missing", Options{ReadOnly: true}); !errors.Is(err, ErrNoBus) {
		t.Fatalf("missing dir: %v", err)
	}
	w, err := OpenAt(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Commit("turn", "c", map[string]string{"text": "hi"}); err != nil {
		t.Fatal(err)
	}

	r, err := OpenAt(dir, Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if !r.ReadOnly() {
		t.Fatal("ReadOnly() false")
	}
	if _, err := r.Commit("turn", "c", nil); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only commit: %v", err)
	}
	r.Publish("turn", "c", nil)
	if r.dropped.Load() != 1 {
		t.Fatalf("publish on a read-only bus must drop, dropped=%d", r.dropped.Load())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sub, _ := r.Subscribe(ctx, 0)
	var got []Frame
	go func() {
		// A second commit after the follower started must stream live.
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Commit("turn", "c", map[string]string{"text": "again"})
	}()
	for f := range sub.Frames() {
		got = append(got, f)
		if len(got) == 2 {
			cancel()
		}
	}
	if len(got) != 2 || got[0].Cursor != 1 || got[1].Cursor != 2 {
		t.Fatalf("follower saw %+v", got)
	}
	if st := r.Stats(); st.Cursor != 2 || !st.Enabled {
		t.Fatalf("read-only stats %+v", st)
	}
	if lines := readLines(t, filepath.Join(dir, activeSegmentName)); len(lines) != 2 {
		t.Fatalf("follower changed the log: %d lines", len(lines))
	}
}

func TestCommitOnClosedOrDisabledBusErrors(t *testing.T) {
	if _, err := disabledBus().Commit("k", "s", nil); err == nil {
		t.Fatal("disabled bus accepted a commit")
	}
	b, err := OpenAt(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = b.Close()
	if _, err := b.Commit("k", "s", nil); err == nil {
		t.Fatal("closed bus accepted a commit")
	}
}
