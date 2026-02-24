package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

var eventLog = logging.ForComponent(logging.CompSession)

// StatusEventWatcher watches ~/.agent-deck/events/ for new status events
// using fsnotify. Delivers parsed StatusEvent structs via a channel.
type StatusEventWatcher struct {
	eventsDir        string
	watcher          *fsnotify.Watcher
	eventCh          chan StatusEvent
	filterInstanceID string // optional: only deliver events for this instance
	ctx              context.Context
	cancel           context.CancelFunc
}

// NewStatusEventWatcher creates a watcher for the events directory.
// If filterInstanceID is non-empty, only events for that instance are delivered.
// Call Start() in a goroutine, then read from EventCh().
func NewStatusEventWatcher(filterInstanceID string) (*StatusEventWatcher, error) {
	eventsDir := GetEventsDir()
	if err := os.MkdirAll(eventsDir, 0755); err != nil {
		return nil, fmt.Errorf("create events dir: %w", err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create fsnotify watcher: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &StatusEventWatcher{
		eventsDir:        eventsDir,
		watcher:          watcher,
		eventCh:          make(chan StatusEvent, 64),
		filterInstanceID: filterInstanceID,
		ctx:              ctx,
		cancel:           cancel,
	}, nil
}

// Start begins watching the events directory. Must be called in a goroutine.
// Blocks until Stop() is called or the context is cancelled.
func (w *StatusEventWatcher) Start() {
	if err := w.watcher.Add(w.eventsDir); err != nil {
		eventLog.Warn("event_watcher_add_failed",
			slog.String("dir", w.eventsDir),
			slog.String("error", err.Error()),
		)
		return
	}

	// Debounce timer: coalesce rapid file events (same pattern as hook_watcher.go)
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

			// Only process .json file writes/creates (not .tmp files)
			if filepath.Ext(event.Name) != ".json" {
				continue
			}
			if event.Op&(fsnotify.Create|fsnotify.Write) == 0 {
				continue
			}

			// If filtering, skip files that don't match
			if w.filterInstanceID != "" {
				base := filepath.Base(event.Name)
				instanceID := strings.TrimSuffix(base, ".json")
				if instanceID != w.filterInstanceID {
					continue
				}
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
					w.processEventFile(f)
				}
			})
			pendingMu.Unlock()

		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			eventLog.Warn("event_watcher_error", slog.String("error", err.Error()))
		}
	}
}

// Stop shuts down the watcher and closes the event channel.
func (w *StatusEventWatcher) Stop() {
	w.cancel()
	_ = w.watcher.Close()
	close(w.eventCh)
}

// EventCh returns the channel that delivers parsed status events.
func (w *StatusEventWatcher) EventCh() <-chan StatusEvent {
	return w.eventCh
}

// WaitForStatus blocks until an event with one of the given statuses is received,
// or the timeout expires.
func (w *StatusEventWatcher) WaitForStatus(statuses []string, timeout time.Duration) (StatusEvent, error) {
	statusSet := make(map[string]bool, len(statuses))
	for _, s := range statuses {
		statusSet[s] = true
	}

	deadline := time.After(timeout)
	for {
		select {
		case event := <-w.eventCh:
			if statusSet[event.Status] {
				return event, nil
			}
		case <-deadline:
			return StatusEvent{}, fmt.Errorf("timeout after %v waiting for status %v", timeout, statuses)
		case <-w.ctx.Done():
			return StatusEvent{}, fmt.Errorf("watcher stopped")
		}
	}
}

// processEventFile reads and parses a single event file, sending the result to eventCh.
func (w *StatusEventWatcher) processEventFile(filePath string) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return
	}

	var event StatusEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return
	}

	// Apply instance filter (belt-and-suspenders with the filename check above)
	if w.filterInstanceID != "" && event.InstanceID != w.filterInstanceID {
		return
	}

	// Non-blocking send: if channel is full, drop the event
	// (consumer should be reading fast enough; 64 buffer is generous)
	select {
	case w.eventCh <- event:
		eventLog.Debug("event_delivered",
			slog.String("instance", event.InstanceID),
			slog.String("status", event.Status),
		)
	default:
		eventLog.Warn("event_channel_full", slog.String("instance", event.InstanceID))
	}
}
