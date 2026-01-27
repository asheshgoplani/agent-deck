package tmux

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// LogWatcher watches session log files for changes using fsnotify
// When a log file is modified, it triggers a callback with the session name
type LogWatcher struct {
	watcher   *fsnotify.Watcher
	logDir    string
	callback  func(sessionName string)
	done      chan struct{}
	closeOnce sync.Once

	// Rate limiting for log events (reduces UI flicker and backend load)
	mu       sync.Mutex
	limiters map[string]*RateLimiter
}

// NewLogWatcher creates a new log file watcher
// callback is called with the session name when its log file changes
func NewLogWatcher(logDir string, callback func(sessionName string)) (*LogWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	// Ensure log directory exists
	if err := os.MkdirAll(logDir, 0755); err != nil {
		w.Close()
		return nil, err
	}

	// Watch the log directory
	if err := w.Add(logDir); err != nil {
		w.Close()
		return nil, err
	}

	return &LogWatcher{
		watcher:  w,
		logDir:   logDir,
		callback: callback,
		done:     make(chan struct{}),
		limiters: make(map[string]*RateLimiter),
	}, nil
}

// Start begins watching for file changes (blocking)
// Call this in a goroutine
func (lw *LogWatcher) Start() {
	for {
		select {
		case <-lw.done:
			return
		case event, ok := <-lw.watcher.Events:
			if !ok {
				return
			}
			// Care about write and create events
			if event.Op&fsnotify.Write == fsnotify.Write || event.Op&fsnotify.Create == fsnotify.Create {
				// Extract session name from filename
				filename := filepath.Base(event.Name)
				if strings.HasSuffix(filename, ".log") {
					sessionName := strings.TrimSuffix(filename, ".log")

					// Get or create rate limiter for this session
					lw.mu.Lock()
					limiter, ok := lw.limiters[sessionName]
					if !ok {
						// 20 events per second limit per session
						limiter = NewRateLimiter(20)
						lw.limiters[sessionName] = limiter
					}
					lw.mu.Unlock()

					// Trigger callback with rate limiting
					limiter.Coalesce(func() {
						lw.callback(sessionName)
					})
				}
			}
		case _, ok := <-lw.watcher.Errors:
			if !ok {
				return
			}
			// Log errors but continue watching
		}
	}
}

// Close stops the watcher
func (lw *LogWatcher) Close() error {
	lw.closeOnce.Do(func() {
		close(lw.done)
	})
	return lw.watcher.Close()
}

// RotateLog truncates a session's log file if it exceeds maxSize
func RotateLog(sessionName string, maxSize int64) error {
	logFile := filepath.Join(LogDir(), sessionName+".log")

	info, err := os.Stat(logFile)
	if err != nil {
		return nil // File doesn't exist, nothing to rotate
	}

	if info.Size() > maxSize {
		// Truncate file (keep last 10KB)
		f, err := os.OpenFile(logFile, os.O_RDWR, 0644)
		if err != nil {
			return err
		}
		defer f.Close()

		keepSize := int64(10 * 1024)
		if info.Size() > keepSize {
			// Seek to position keepSize bytes from end
			_, err = f.Seek(-keepSize, io.SeekEnd)
			if err != nil {
				return err
			}

			// Read the tail
			tail := make([]byte, keepSize)
			n, err := f.Read(tail)
			if err != nil && err != io.EOF {
				return err
			}

			// Truncate and write tail at beginning
			if err := f.Truncate(0); err != nil {
				return err
			}
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return err
			}
			if _, err := f.Write(tail[:n]); err != nil {
				return err
			}
		}
	}
	return nil
}
