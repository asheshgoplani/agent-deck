package costs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
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
}

// CostEventWatcher watches a directory for new cost event JSON files.
type CostEventWatcher struct {
	dir     string
	watcher *fsnotify.Watcher
	eventCh chan RawCostEvent
	ctx     context.Context
	cancel  context.CancelFunc
}

// NewCostEventWatcher creates a watcher for the given directory.
func NewCostEventWatcher(dir string) (*CostEventWatcher, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	w, err := fsnotify.NewWatcher()
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
	defer close(w.eventCh)
	var mu sync.Mutex
	pending := make(map[string]struct{})
	var timer *time.Timer

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

	select {
	case w.eventCh <- ev:
		os.Remove(path) // only delete after successful send
	default:
		// channel full, leave file for retry on next fsnotify event
	}
}
