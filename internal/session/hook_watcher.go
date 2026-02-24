package session

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

var hookLog = logging.ForComponent(logging.CompSession)

// HookStatus holds the decoded status from a hook status file.
type HookStatus struct {
	Status    string    // running, idle, waiting, dead
	SessionID string    // Claude session ID
	Event     string    // Hook event name
	UpdatedAt time.Time // When this status was received
}

// StatusFileWatcher watches ~/.agent-deck/hooks/ for status file changes
// and updates instance hook status in real time.
type StatusFileWatcher struct {
	hooksDir string
	watcher  *fsnotify.Watcher

	mu       sync.RWMutex
	statuses map[string]*HookStatus // instance_id -> latest hook status

	ctx    context.Context
	cancel context.CancelFunc

	// onChange is called when a hook status changes (for TUI refresh)
	onChange func()
}

// NewStatusFileWatcher creates a new watcher for the hooks directory.
// Call Start() to begin watching.
func NewStatusFileWatcher(onChange func()) (*StatusFileWatcher, error) {
	hooksDir := GetHooksDir()

	// Ensure directory exists
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return nil, err
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &StatusFileWatcher{
		hooksDir: hooksDir,
		watcher:  watcher,
		statuses: make(map[string]*HookStatus),
		ctx:      ctx,
		cancel:   cancel,
		onChange: onChange,
	}, nil
}

// Start begins watching the hooks directory. Must be called in a goroutine.
func (w *StatusFileWatcher) Start() {
	if err := w.watcher.Add(w.hooksDir); err != nil {
		hookLog.Warn("hook_watcher_add_failed", slog.String("dir", w.hooksDir), slog.String("error", err.Error()))
		return
	}

	// Load any existing status files on startup
	w.loadExisting()

	// Debounce timer: coalesce rapid file events
	var debounceTimer *time.Timer
	pendingFiles := make(map[string]bool)
	var pendingMu sync.Mutex

	for {
		select {
		case <-w.ctx.Done():
			return

		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}

			// Only process .json file writes/creates
			if filepath.Ext(event.Name) != ".json" {
				continue
			}
			if event.Op&(fsnotify.Create|fsnotify.Write) == 0 {
				continue
			}

			pendingMu.Lock()
			pendingFiles[event.Name] = true
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(100*time.Millisecond, func() {
				pendingMu.Lock()
				files := make([]string, 0, len(pendingFiles))
				for f := range pendingFiles {
					files = append(files, f)
				}
				pendingFiles = make(map[string]bool)
				pendingMu.Unlock()

				for _, f := range files {
					w.processFile(f)
				}
			})
			pendingMu.Unlock()

		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			hookLog.Warn("hook_watcher_error", slog.String("error", err.Error()))
		}
	}
}

// Stop shuts down the watcher.
func (w *StatusFileWatcher) Stop() {
	w.cancel()
	_ = w.watcher.Close()
	// Clear statuses map to release memory
	w.mu.Lock()
	w.statuses = nil
	w.mu.Unlock()
}

// GetHookStatus returns the hook status for an instance, or nil if not available.
func (w *StatusFileWatcher) GetHookStatus(instanceID string) *HookStatus {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.statuses[instanceID]
}

// loadExisting reads all current status files on startup.
func (w *StatusFileWatcher) loadExisting() {
	entries, err := os.ReadDir(w.hooksDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		w.processFile(filepath.Join(w.hooksDir, entry.Name()))
	}
}

// processFile reads a status file and updates the internal map.
func (w *StatusFileWatcher) processFile(filePath string) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return
	}

	var status struct {
		Status    string `json:"status"`
		SessionID string `json:"session_id"`
		Event     string `json:"event"`
		Timestamp int64  `json:"ts"`
	}
	if err := json.Unmarshal(data, &status); err != nil {
		return
	}

	// Extract instance ID from filename (remove .json extension)
	base := filepath.Base(filePath)
	instanceID := strings.TrimSuffix(base, ".json")

	hookStatus := &HookStatus{
		Status:    status.Status,
		SessionID: status.SessionID,
		Event:     status.Event,
		UpdatedAt: time.Unix(status.Timestamp, 0),
	}

	w.mu.Lock()
	w.statuses[instanceID] = hookStatus
	w.mu.Unlock()

	hookLog.Debug("hook_status_updated",
		slog.String("instance", instanceID),
		slog.String("status", status.Status),
		slog.String("event", status.Event),
	)

	if w.onChange != nil {
		w.onChange()
	}
}

// GetHooksDir returns the path to the hooks status directory.
func GetHooksDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".agent-deck", "hooks")
	}
	return filepath.Join(home, ".agent-deck", "hooks")
}
