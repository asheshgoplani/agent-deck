// Package fswatch keeps directory watches bounded on kqueue platforms, where
// fsnotify opens a descriptor for every file in a watched directory. Polling
// retains metadata only; other platforms keep native fsnotify notifications.
package fswatch

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher reports changes to explicitly added files and immediate directory
// entries. It has the fsnotify surface used by the application's watchers.
// Polling may coalesce changes between scans, including short-lived files.
type Watcher struct {
	Events chan fsnotify.Event
	Errors chan error
	native *fsnotify.Watcher
	mu     sync.Mutex
	paths  map[string]snapshot
	closed bool
	stop   chan struct{}
	done   chan struct{}

	inPlaceWrites bool
	beforeScan    func() // test seam: runs before each poll's scan
}

// Options adjusts a Watcher.
type Options struct {
	// InPlaceWrites is for directories whose files are written in place
	// (appended to or rewritten without a rename), such as Claude's
	// transcripts under ~/.claude/projects. Such a write does not change
	// the directory's mtime, so the poller then reads every entry on every
	// poll instead of skipping a directory whose mtime is unchanged. The
	// native watchers report in-place writes anyway and ignore it.
	InPlaceWrites bool
}

const pollInterval = 250 * time.Millisecond

// forcePolling selects the polling watcher on every platform (fswatchpoll
// build tag, for tests).
var forcePolling bool

// SetForcePollingForTest makes every new Watcher a polling one, as on
// macOS, so other packages' tests can run the poller on Linux (the
// fswatchpoll build tag does the same for a whole binary). It returns a
// func that restores the previous setting. Not for production code.
func SetForcePollingForTest(on bool) (restore func()) {
	prev := forcePolling
	forcePolling = on
	return func() { forcePolling = prev }
}

// NewWatcher uses polling where native directory watches retain per-file fds.
func NewWatcher() (*Watcher, error) {
	return newWatcher(runtime.GOOS)
}

// NewWatcherWithOptions is NewWatcher with opts.
func NewWatcherWithOptions(opts Options) (*Watcher, error) {
	return newWatcherOpts(runtime.GOOS, opts)
}

func newWatcher(goos string) (*Watcher, error) { return newWatcherOpts(goos, Options{}) }

func newWatcherOpts(goos string, opts Options) (*Watcher, error) {
	switch goos {
	case "darwin", "freebsd", "openbsd", "netbsd", "dragonfly":
		return newPollingWatcherWith(pollInterval, opts, nil), nil
	default:
		if forcePolling {
			return newPollingWatcherWith(pollInterval, opts, nil), nil
		}
		native, err := fsnotify.NewWatcher()
		if err != nil {
			return nil, err
		}
		return &Watcher{Events: native.Events, Errors: native.Errors, native: native}, nil
	}
}

func newPollingWatcher(interval time.Duration) *Watcher {
	return newPollingWatcherWith(interval, Options{}, nil)
}

func newPollingWatcherWith(interval time.Duration, opts Options, beforeScan func()) *Watcher {
	w := &Watcher{
		Events: make(chan fsnotify.Event, 64), Errors: make(chan error, 1),
		paths: make(map[string]snapshot),
		stop:  make(chan struct{}), done: make(chan struct{}),
		inPlaceWrites: opts.InPlaceWrites,
		beforeScan:    beforeScan,
	}
	go w.poll(interval)
	return w
}

// Add starts watching path without replaying its existing contents.
func (w *Watcher) Add(path string) error {
	if w.native != nil {
		return w.native.Add(path)
	}
	path = filepath.Clean(path)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return fsnotify.ErrClosed
	}
	if _, exists := w.paths[path]; exists {
		return nil
	}
	current, err := snapshotPath(path)
	if err != nil {
		return err
	}
	w.paths[path] = current
	return nil
}

// Remove stops watching path. Events already queued may still be delivered.
func (w *Watcher) Remove(path string) error {
	if w.native != nil {
		return w.native.Remove(path)
	}
	path = filepath.Clean(path)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return fsnotify.ErrClosed
	}
	if _, exists := w.paths[path]; !exists {
		return fsnotify.ErrNonExistentWatch
	}
	delete(w.paths, path)
	return nil
}

// WatchList returns the explicitly registered paths.
func (w *Watcher) WatchList() []string {
	if w.native != nil {
		return w.native.WatchList()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	paths := make([]string, 0, len(w.paths))
	for path := range w.paths {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// Close releases resources and closes channels. It does not wait for consumers
// to drain pending events, and may safely be called more than once.
func (w *Watcher) Close() error {
	if w.native != nil {
		return w.native.Close()
	}
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		w.paths = nil
		close(w.stop)
	}
	w.mu.Unlock()
	<-w.done
	return nil
}

// snapshot is a watched path as last read: its own info and, for a
// directory, its immediate entries, keyed by full path.
type snapshot struct {
	entries map[string]os.FileInfo
	readAt  time.Time // taken before the path itself was read
}

// snapshotPath uses Lstat for both the target and its immediate entries. A
// symlink is observed as a link, never expanded into the target's contents.
func snapshotPath(path string) (snapshot, error) {
	readAt := time.Now()
	info, err := os.Lstat(path)
	if err != nil {
		return snapshot{}, err
	}
	current := snapshot{entries: map[string]os.FileInfo{path: info}, readAt: readAt}
	if !info.IsDir() {
		return current, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return snapshot{}, err
	}
	for _, entry := range entries {
		child := filepath.Join(path, entry.Name())
		info, err := os.Lstat(child)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return snapshot{}, err
		}
		current.entries[child] = info
	}
	return current, nil
}

// scanShare caps the time spent reading at 1/scanShare of the time between
// reads: a large directory that keeps changing (hook writes while agents
// work) is read less often instead of costing a share of a core that grows
// with its size.
const scanShare = 25

// maxPollWait bounds that backoff: one slow scan (a stalled disk, a
// suspended laptop waking up) must not leave changes unreported for 25
// times as long.
const maxPollWait = 2 * time.Second

// nextPollWait is the wait before the next scan after one that took took.
func nextPollWait(interval, took time.Duration) time.Duration {
	return max(interval, min(maxPollWait, scanShare*took))
}

func (w *Watcher) poll(interval time.Duration) {
	defer close(w.done)
	defer close(w.Events)
	defer close(w.Errors)
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-timer.C:
			start := time.Now()
			if w.beforeScan != nil {
				w.beforeScan()
			}
			events, errors := w.scan()
			timer.Reset(nextPollWait(interval, time.Since(start)))
			for _, err := range errors {
				select {
				case w.Errors <- err:
				default:
				}
			}
			for _, event := range events {
				select {
				case w.Events <- event:
				case <-w.stop:
					return
				}
			}
		}
	}
}

func (w *Watcher) scan() ([]fsnotify.Event, []error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var events []fsnotify.Event
	var errors []error
	for path, previous := range w.paths {
		if !w.inPlaceWrites && dirUnchanged(path, previous) {
			continue
		}
		current, err := snapshotPath(path)
		if os.IsNotExist(err) {
			events = append(events, fsnotify.Event{Name: path, Op: fsnotify.Remove})
			delete(w.paths, path)
			continue
		}
		if err != nil {
			errors = append(errors, err)
			continue
		}
		for name, info := range current.entries {
			old, exists := previous.entries[name]
			if !exists {
				events = append(events, fsnotify.Event{Name: name, Op: fsnotify.Create})
			} else if changed(old, info) {
				events = append(events, fsnotify.Event{Name: name, Op: fsnotify.Write})
			}
		}
		for name := range previous.entries {
			if _, exists := current.entries[name]; !exists {
				events = append(events, fsnotify.Event{Name: name, Op: fsnotify.Remove})
			}
		}
		w.paths[path] = current
	}
	return events, errors
}

// dirUnchanged reports whether path is still the directory last read with the
// same mtime, so its entries need no Lstat each: creating, removing or renaming
// an entry updates the directory's mtime, and the hook, event, cost and
// session-anchor writers replace their files by rename. An in-place write to
// an existing entry (an append) does not, so without Options.InPlaceWrites it
// is reported only with the directory's next change; watch the file itself,
// or set InPlaceWrites (global search does, for Claude's transcripts), to see
// it at once.
//
// File systems stamp times at a coarse granularity (a kernel tick on Linux,
// whole seconds on some), so a change made right after the last read can
// keep the same mtime. An mtime is trusted only once the last read came
// clearly after it; until then the directory is read in full.
func dirUnchanged(path string, last snapshot) bool {
	prev := last.entries[path]
	if prev == nil || !prev.IsDir() {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil || changed(prev, info) || !info.ModTime().Equal(prev.ModTime()) {
		return false
	}
	granularity := 100 * time.Millisecond
	if info.ModTime().Nanosecond() == 0 {
		granularity = 2 * time.Second // whole-second (or FAT's 2 s) timestamps
	}
	return info.ModTime().Add(granularity).Before(last.readAt)
}

func changed(old, current os.FileInfo) bool {
	if !os.SameFile(old, current) || old.Mode() != current.Mode() {
		return true
	}
	// Directory mtime changes are represented by their entry events.
	if current.IsDir() {
		return false
	}
	return old.Size() != current.Size() || !old.ModTime().Equal(current.ModTime())
}
