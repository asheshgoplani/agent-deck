package events

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// reviewFrames builds n queued frames for direct writeBatch calls.
func reviewFrames(n int, kind string) []queuedFrame {
	out := make([]queuedFrame, n)
	for i := range out {
		out[i] = queuedFrame{kind: kind, sessionID: "s", ts: time.Now()}
	}
	return out
}

// reviewCheckDir asserts the on-disk invariants a follower relies on: every
// sealed segment's name matches its contents, and every cursor 1..N appears
// exactly once across all files.
func reviewCheckDir(t *testing.T, dir string) []Cursor {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "*.ndjson"))
	seen := map[Cursor]string{}
	var all []Cursor
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var cursors []Cursor
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fr, err := ParseFrameLine(sc.Bytes())
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			cursors = append(cursors, fr.Cursor)
			if prev, dup := seen[fr.Cursor]; dup {
				t.Errorf("cursor %d duplicated in %s and %s", fr.Cursor, filepath.Base(prev), filepath.Base(path))
			}
			seen[fr.Cursor] = path
			all = append(all, fr.Cursor)
		}
		f.Close()
		base := filepath.Base(path)
		if strings.HasPrefix(base, "seg-") {
			var s, e uint64
			fmt.Sscanf(base, "seg-%020d-%020d.ndjson", &s, &e)
			if len(cursors) == 0 {
				t.Errorf("sealed %s is empty", base)
			} else if uint64(cursors[0]) != s || uint64(cursors[len(cursors)-1]) != e {
				t.Errorf("sealed %s holds cursors %d..%d", base, cursors[0], cursors[len(cursors)-1])
			}
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	for i, c := range all {
		if c != Cursor(i+1) {
			t.Errorf("cursor sequence broken at index %d: got %d", i, c)
			break
		}
	}
	return all
}

// Two writers on one profile (the TUI's Default bus and its watcher Engine
// bus, or the TUI and the notify daemon). A crosses the rotation threshold
// without a sync, B appends, then A's sync tick runs with an empty batch.
func TestReviewStaleRotationAcrossWritersDirect(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a.maxSegFrames, b.maxSegFrames = 4, 4
	a.writeBatch(reviewFrames(4, "a"), false)
	b.writeBatch(reviewFrames(2, "b"), false)
	a.writeBatch(nil, true) // A's one-second sync tick
	a.writeBatch(reviewFrames(1, "a"), false)
	b.writeBatch(reviewFrames(1, "b"), false)
	b.writeBatch(nil, true)
	_ = a.Close()
	_ = b.Close()
	reviewCheckDir(t, dir)
}

// The second writer runs in another process, as it does when a TUI and
// notify daemon share a profile. Its rotation must invalidate A's view.
func TestRotationAcrossWriterProcesses(t *testing.T) {
	if dir := os.Getenv("EVENTS_ROTATION_CHILD_DIR"); dir != "" {
		b, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		b.maxSegFrames = 4
		b.writeBatch(reviewFrames(2, "child"), true)
		if err := b.Close(); err != nil {
			t.Fatal(err)
		}
		return
	}

	dir := t.TempDir()
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a.maxSegFrames = 4
	a.writeBatch(reviewFrames(4, "parent"), false)
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-test.run=^TestRotationAcrossWriterProcesses$")
	cmd.Env = append(os.Environ(), "EVENTS_ROTATION_CHILD_DIR="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("second writer: %v: %s", err, out)
	}
	a.writeBatch(nil, true) // stale one-second sync tick after B rotated
	a.writeBatch(reviewFrames(1, "parent"), true)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	all := reviewCheckDir(t, dir)
	if len(all) != 7 {
		t.Fatalf("on-disk cursors = %v, want exactly 1..7", all)
	}

	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	sub, _ := r.Subscribe(ctx, 4)
	var got []Cursor
	for f := range sub.Frames() {
		got = append(got, f.Cursor)
	}
	if len(got) != 3 || got[0] != 5 || got[1] != 6 || got[2] != 7 {
		t.Fatalf("resume after 4 = %v, want [5 6 7]", got)
	}
}

// Same race through the real writer loops and tickers only (public API).
func TestReviewStaleRotationAcrossWritersRealTicks(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a.maxSegFrames, b.maxSegFrames = 4, 4
	for i := 0; i < 4; i++ {
		a.Publish("a", "s", nil)
	}
	time.Sleep(200 * time.Millisecond) // appended by A's 50ms tick, not synced
	for i := 0; i < 2; i++ {
		b.Publish("b", "s", nil)
	}
	time.Sleep(2500 * time.Millisecond) // both one-second sync ticks
	a.Publish("a", "s", nil)
	time.Sleep(200 * time.Millisecond)
	b.Publish("b", "s", nil)
	time.Sleep(200 * time.Millisecond)
	_ = a.Close()
	_ = b.Close()
	all := reviewCheckDir(t, dir)
	t.Logf("cursors on disk: %v", all)

	// Resume the way `events follow --after 4` would.
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	sub, _ := r.Subscribe(ctx, 4)
	var got []Cursor
	for f := range sub.Frames() {
		got = append(got, f.Cursor)
	}
	t.Logf("resume --after 4 saw %v", got)
	if len(got) != 4 || got[0] != 5 || got[3] != 8 {
		t.Errorf("resume after 4 = %v, want [5 6 7 8]", got)
	}
}
