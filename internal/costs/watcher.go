package costs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/asheshgoplani/agent-deck/internal/fswatch"
)

// RawCostEvent is the JSON structure written by hook_handler.
type RawCostEvent struct {
	InstanceID       string `json:"instance_id"`
	Model            string `json:"model"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	Timestamp        int64  `json:"ts"`
	// MessageUUID is the transcript record the usage was read from (empty
	// from hooks of older binaries).
	MessageUUID string `json:"message_uuid,omitempty"`
	// MessageID is the API message id of that record.
	MessageID string `json:"message_id,omitempty"`
}

// HookCostEventID is the cost_events id of a Stop hook's usage: the id the
// recall importer gives the same message (UsageEventID), so a turn seen by
// both is stored once. Without a message id or record uuid (hooks of older
// binaries) it falls back to "<instance>_<unix nanos>".
func HookCostEventID(instanceID, messageID, recordUUID string, ts int64) string {
	if messageID != "" || recordUUID != "" {
		return UsageEventID(instanceID, messageID, recordUUID)
	}
	return fmt.Sprintf("%s_%d", instanceID, ts)
}

// CostEventWatcher watches a directory for new cost event JSON files.
type CostEventWatcher struct {
	dir     string
	watcher *fswatch.Watcher
	eventCh chan RawCostEvent
	ctx     context.Context
	cancel  context.CancelFunc

	// sendMu guards closed and every send on eventCh, so a debounce timer
	// that fires after Start returned cannot send on the closed channel.
	sendMu sync.Mutex
	closed bool
}

// NewCostEventWatcher creates a watcher for the given directory.
func NewCostEventWatcher(dir string) (*CostEventWatcher, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	w, err := fswatch.NewWatcher()
	if err != nil {
		return nil, err
	}

	if err := w.Add(dir); err != nil {
		w.Close()
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &CostEventWatcher{
		dir:     dir,
		watcher: w,
		eventCh: make(chan RawCostEvent, 64),
		ctx:     ctx,
		cancel:  cancel,
	}, nil
}

// EventCh returns the channel that emits parsed cost events.
func (w *CostEventWatcher) EventCh() <-chan RawCostEvent {
	return w.eventCh
}

// Start begins watching for file events. Blocks until stopped.
func (w *CostEventWatcher) Start() {
	var mu sync.Mutex
	pending := make(map[string]struct{})
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
		w.sendMu.Lock()
		w.closed = true
		close(w.eventCh)
		w.sendMu.Unlock()
	}()

	processPending := func() {
		mu.Lock()
		files := make([]string, 0, len(pending))
		for f := range pending {
			files = append(files, f)
		}
		pending = make(map[string]struct{})
		mu.Unlock()

		for _, f := range files {
			w.processFile(f)
		}
	}

	for {
		select {
		case <-w.ctx.Done():
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Create|fsnotify.Write) == 0 {
				continue
			}
			if filepath.Ext(event.Name) != ".json" || strings.HasSuffix(event.Name, ".tmp") {
				continue
			}
			mu.Lock()
			pending[event.Name] = struct{}{}
			mu.Unlock()

			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(100*time.Millisecond, processPending)
		case <-w.watcher.Errors:
			// continue
		}
	}
}

// Stop cancels the watcher and closes resources.
func (w *CostEventWatcher) Stop() {
	w.cancel()
	w.watcher.Close()
}

func (w *CostEventWatcher) processFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var ev RawCostEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		os.Remove(path) // malformed, remove
		return
	}

	w.sendMu.Lock()
	defer w.sendMu.Unlock()
	if w.closed {
		return // shutting down: leave the file for the next run
	}
	select {
	case w.eventCh <- ev:
		os.Remove(path) // only delete after successful send
	default:
		// channel full, leave file for retry on next fsnotify event
	}
}
