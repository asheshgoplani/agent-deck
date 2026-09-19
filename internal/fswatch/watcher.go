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
	paths  map[string]map[string]os.FileInfo
	closed bool
	stop   chan struct{}
	done   chan struct{}
}

// NewWatcher uses polling where native directory watches retain per-file fds.
func NewWatcher() (*Watcher, error) {
	return newWatcher(runtime.GOOS)
}

func newWatcher(goos string) (*Watcher, error) {
	switch goos {
	case "darwin", "freebsd", "openbsd", "netbsd", "dragonfly":
		return newPollingWatcher(250 * time.Millisecond), nil
	default:
		native, err := fsnotify.NewWatcher()
		if err != nil {
			return nil, err
		}
		return &Watcher{Events: native.Events, Errors: native.Errors, native: native}, nil
	}
}

func newPollingWatcher(interval time.Duration) *Watcher {
	w := &Watcher{
		Events: make(chan fsnotify.Event, 64), Errors: make(chan error, 1),
		paths: make(map[string]map[string]os.FileInfo),
		stop:  make(chan struct{}), done: make(chan struct{}),
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
	snapshot, err := snapshotPath(path)
	if err != nil {
		return err
	}
	w.paths[path] = snapshot
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

// snapshotPath uses Lstat for both the target and its immediate entries. A
// symlink is observed as a link, never expanded into the target's contents.
func snapshotPath(path string) (map[string]os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	snapshot := map[string]os.FileInfo{path: info}
	if !info.IsDir() {
		return snapshot, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		child := filepath.Join(path, entry.Name())
		info, err := os.Lstat(child)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		snapshot[child] = info
	}
	return snapshot, nil
}

func (w *Watcher) poll(interval time.Duration) {
	defer close(w.done)
	defer close(w.Events)
	defer close(w.Errors)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			events, errors := w.scan()
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
		for name, info := range current {
			old, exists := previous[name]
			if !exists {
				events = append(events, fsnotify.Event{Name: name, Op: fsnotify.Create})
			} else if changed(old, info) {
				events = append(events, fsnotify.Event{Name: name, Op: fsnotify.Write})
			}
		}
		for name := range previous {
			if _, exists := current[name]; !exists {
				events = append(events, fsnotify.Event{Name: name, Op: fsnotify.Remove})
			}
		}
		w.paths[path] = current
	}
	return events, errors
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
