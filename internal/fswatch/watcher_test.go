package fswatch

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func awaitEvent(t *testing.T, w *Watcher, path string, op fsnotify.Op) {
	t.Helper()
	select {
	case event := <-w.Events:
		if event.Name != path || event.Op&op == 0 {
			t.Fatalf("got %v, want %s %v", event, path, op)
		}
	case err := <-w.Errors:
		t.Fatalf("watch error: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatalf("no %v event for %s", op, path)
	}
}

func TestPollingChangesAndAtomicReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "event.json")
	w := newPollingWatcher(10 * time.Millisecond)
	defer w.Close()
	if err := w.Add(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	awaitEvent(t, w, path, fsnotify.Create)
	// Preserve size and mtime to prove replacement detection uses identity.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(t.TempDir(), "replacement")
	if err := os.WriteFile(replacement, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	awaitEvent(t, w, path, fsnotify.Write)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	awaitEvent(t, w, path, fsnotify.Remove)
}

func TestPollingDirectoryDoesNotTraverseSymlinks(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	target := filepath.Join(outside, "target")
	if err := os.WriteFile(target, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "directory-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "file-link")); err != nil {
		t.Fatal(err)
	}
	w := newPollingWatcher(10 * time.Millisecond)
	defer w.Close()
	if err := w.Add(dir); err != nil {
		t.Fatal(err)
	}
	if err := w.Add(filepath.Join(dir, "directory-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("changed outside"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-w.Events:
		t.Fatalf("followed symlink: %v", event)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestPollingDirectFileAndRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	w := newPollingWatcher(10 * time.Millisecond)
	defer w.Close()
	if err := w.Add(path); err != nil {
		t.Fatal(err)
	}
	if err := w.Add(path); err != nil {
		t.Fatal(err)
	}
	if len(w.WatchList()) != 1 {
		t.Fatalf("duplicate watch: %v", w.WatchList())
	}
	if err := os.WriteFile(path, []byte("longer"), 0600); err != nil {
		t.Fatal(err)
	}
	awaitEvent(t, w, path, fsnotify.Write)
	if err := w.Remove(path); err != nil {
		t.Fatal(err)
	}
	if len(w.WatchList()) != 0 {
		t.Fatal("watch remains after removal")
	}
}

func TestPollingThousandOrphansBoundedFDs(t *testing.T) {
	if _, err := os.ReadDir("/proc/self/fd"); err != nil {
		t.Skip("Linux descriptor inspection required")
	}
	dir := t.TempDir()
	for i := 0; i < 1000; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("orphan-%04d.json", i)), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Exercise the production macOS selection on Linux as well.
	w, err := newWatcher("darwin")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if w.native != nil {
		t.Fatal("macOS selected a per-file native watcher")
	}
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Add(dir); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	after, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("polling 1000 orphan files: fd before=%d after=%d delta=%d", len(before), len(after), len(after)-len(before))
	if len(after)-len(before) > 2 {
		t.Fatalf("descriptor growth: before=%d after=%d", len(before), len(after))
	}
}

func TestPollingCloseWithUnreadEvents(t *testing.T) {
	dir := t.TempDir()
	w := newPollingWatcher(10 * time.Millisecond)
	if err := w.Add(dir); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprint(i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(50 * time.Millisecond)
	closed := make(chan struct{})
	go func() { _ = w.Close(); _ = w.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Close blocked on unread events")
	}
	if err := w.Add(dir); err == nil {
		t.Fatal("Add after Close succeeded")
	}
	for range w.Events {
	}
	for range w.Errors {
	}
}

// An idle directory must cost the poller O(1) per scan, not one Lstat (and its
// allocations) per entry: macOS polls the shared hooks directory, which keeps
// several artifacts for every registered session, four times a second.
func TestPollingIdleDirectoryScanIsConstant(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 2000; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("session-%04d.json", i)), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Older than the coarse-timestamp window, as an idle directory is.
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	w := newPollingWatcher(time.Hour) // scans run only when the test calls scan
	defer w.Close()
	if err := w.Add(dir); err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(5, func() {
		if events, errs := w.scan(); len(events) != 0 || len(errs) != 0 {
			t.Fatalf("idle scan reported events=%v errors=%v", events, errs)
		}
	})
	t.Logf("idle scan of 2000 entries: %.0f allocations", allocs)
	if allocs > 20 {
		t.Fatalf("idle scan of 2000 entries made %.0f allocations, want O(1) (at most 20)", allocs)
	}
}

// A file system with whole-second timestamps can leave a directory's mtime
// unchanged by a second change in the same second; a recent whole-second mtime
// must not hide that change.
func TestPollingCoarseDirectoryMtimeStillSeesChange(t *testing.T) {
	dir := t.TempDir()
	coarse := time.Now().Truncate(time.Second)
	if err := os.Chtimes(dir, coarse, coarse); err != nil {
		t.Fatal(err)
	}
	w := newPollingWatcher(time.Hour) // scans run only when the test calls scan
	defer w.Close()
	if err := w.Add(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "event.json")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dir, coarse, coarse); err != nil {
		t.Fatal(err)
	}
	events, errs := w.scan()
	if len(errs) != 0 || len(events) != 1 || events[0].Name != path || events[0].Op != fsnotify.Create {
		t.Fatalf("got events=%v errors=%v, want one Create for %s", events, errs, path)
	}
}

// One slow scan backs the poller off, but never by more than maxPollWait: a
// 400 ms stall would otherwise mean 10 s (25x) with changes unreported.
func TestPollingSlowScanBackoffIsCapped(t *testing.T) {
	var mu sync.Mutex
	var starts []time.Time
	beforeScan := func() {
		mu.Lock()
		starts = append(starts, time.Now())
		first := len(starts) == 1
		mu.Unlock()
		if first {
			time.Sleep(400 * time.Millisecond)
		}
	}
	w := newPollingWatcherWith(10*time.Millisecond, Options{}, beforeScan)
	defer w.Close()
	if err := w.Add(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(maxPollWait + 2*time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(starts)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(starts) < 2 {
		t.Fatalf("no scan within %v of a 400ms scan, want the backoff capped at %v", maxPollWait+2*time.Second, maxPollWait)
	}
	if gap := starts[1].Sub(starts[0]); gap > 400*time.Millisecond+maxPollWait+500*time.Millisecond {
		t.Fatalf("next scan %v after a 400ms scan, want at most the scan plus %v", gap, maxPollWait)
	}
	if got := nextPollWait(250*time.Millisecond, 2*time.Second); got != maxPollWait {
		t.Fatalf("nextPollWait after a 2s scan = %v, want %v", got, maxPollWait)
	}
	if got := nextPollWait(250*time.Millisecond, time.Millisecond); got != 250*time.Millisecond {
		t.Fatalf("nextPollWait after a 1ms scan = %v, want the interval", got)
	}
}

// Claude appends to its transcripts in place, which leaves the directory's
// mtime alone. With InPlaceWrites the append in an otherwise idle directory
// is reported on the next scan; without it the idle directory is skipped.
func TestPollingInPlaceAppendInIdleDirectory(t *testing.T) {
	for _, inPlace := range []bool{true, false} {
		dir := t.TempDir()
		path := filepath.Join(dir, "session.jsonl")
		if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-time.Minute)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
		w := newPollingWatcherWith(time.Hour, Options{InPlaceWrites: inPlace}, nil)
		if err := w.Add(dir); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString("{\"type\":\"assistant\"}\n"); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
		if info, _ := os.Lstat(dir); !info.ModTime().Equal(old) {
			t.Fatalf("the append moved the directory mtime; the test needs an idle directory")
		}
		events, errs := w.scan()
		_ = w.Close()
		reported := len(errs) == 0 && len(events) == 1 && events[0].Name == path && events[0].Op == fsnotify.Write
		if inPlace && !reported {
			t.Fatalf("InPlaceWrites: got events=%v errors=%v, want one Write for %s", events, errs, path)
		}
		if !inPlace && len(events) != 0 {
			t.Fatalf("default watcher read an idle directory: %v", events)
		}
	}
}
