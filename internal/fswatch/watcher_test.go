package fswatch

import (
	"fmt"
	"os"
	"path/filepath"
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
