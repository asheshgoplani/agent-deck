package session

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

var kanbanLog = logging.ForComponent("hermes-kanban")

const (
	kanbanInitialBackoff = 1 * time.Second
	kanbanMaxBackoff     = 30 * time.Second
	kanbanBackoffFactor  = 2

	// kanbanWSReadLimit bounds a single WebSocket frame the gateway can push.
	// Same 1 MB cap used by seedCounts for the HTTP board endpoint.
	kanbanWSReadLimit = 1 << 20

	// kanbanReadTimeout is how long readEvents will wait between frames or pong
	// responses before declaring the connection half-open and forcing a reconnect.
	kanbanReadTimeout = 60 * time.Second

	// kanbanPingInterval is how often we send a ping while the WS is idle.
	// Must be < kanbanReadTimeout so a pong can reset the deadline in time.
	kanbanPingInterval = 25 * time.Second
)

// kanbanSeedClient is shared across all seedCounts calls so TCP connection
// reuse and TLS session caching survive reconnects.
var kanbanSeedClient = &http.Client{Timeout: 10 * time.Second}

// kanbanEvent is the minimal shape of a Hermes Kanban WebSocket event.
type kanbanEvent struct {
	ID     int64  `json:"id"`
	Kind   string `json:"kind"`
	TaskID string `json:"task_id"`
}

// kanbanBoardResponse is used to seed initial counts from the HTTP board endpoint.
type kanbanBoardResponse struct {
	Tasks []kanbanTask `json:"tasks"`
}

type kanbanTask struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// KanbanWatcher maintains a live count of running/blocked Kanban tasks
// by streaming events from the Hermes gateway WebSocket endpoint.
// Falls back gracefully when the gateway is unreachable.
type KanbanWatcher struct {
	gatewayURL string

	mu           sync.RWMutex
	running      int
	blocked      int
	taskStatuses map[string]string
	seedOK       bool // true after at least one successful seedCounts

	lastEventID int64

	stopCh    chan struct{}
	stopOnce  sync.Once
	startOnce sync.Once
	subsMu    sync.Mutex
	subs      []chan struct{}

	// droppedNotifications counts notify() calls where a subscriber's buffered
	// channel was already full — i.e. the subscriber didn't drain in time.
	// Exposed via DroppedNotifications() for observability.
	droppedNotifications atomic.Int64
}

// NewKanbanWatcher creates a new KanbanWatcher for the given gateway URL.
// The URL should be the HTTP/WS base URL of the Hermes gateway
// (e.g. "http://127.0.0.1:8080" or "ws://127.0.0.1:8080").
func NewKanbanWatcher(gatewayURL string) *KanbanWatcher {
	return &KanbanWatcher{
		gatewayURL:   gatewayURL,
		taskStatuses: make(map[string]string),
		stopCh:       make(chan struct{}),
	}
}

// Start runs the reconnect loop in a goroutine. Idempotent — safe to call multiple times.
func (w *KanbanWatcher) Start() {
	w.startOnce.Do(func() {
		go w.reconnectLoop()
	})
}

// Stop signals the watcher to stop and closes all subscriber channels so
// goroutines blocked in listenForKanbanUpdates unblock immediately. Idempotent.
func (w *KanbanWatcher) Stop() {
	w.stopOnce.Do(func() {
		close(w.stopCh)
		// Close subscriber channels so any goroutine blocked on <-ch returns.
		w.subsMu.Lock()
		for _, ch := range w.subs {
			close(ch)
		}
		w.subs = nil
		w.subsMu.Unlock()
	})
}

// Counts returns the current running and blocked task counts.
// Always instant — reads from in-memory state protected by RWMutex.
func (w *KanbanWatcher) Counts() (running, blocked int) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.running, w.blocked
}

// TaskStatus returns the current status of a specific task by ID.
// Returns "running", "blocked", or "" (not found / terminal state).
func (w *KanbanWatcher) TaskStatus(id string) string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.taskStatuses == nil {
		return ""
	}
	return w.taskStatuses[id]
}

// IsHealthy returns true if the watcher has successfully seeded from the gateway
// at least once. When false, callers should fall back to CLI polling.
func (w *KanbanWatcher) IsHealthy() bool {
	if w == nil {
		return false
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.seedOK
}

// Subscribe returns a channel that receives an empty struct whenever the
// running or blocked count changes. The channel is buffered (capacity 1);
// slow consumers miss coalesced updates but never block the watcher.
func (w *KanbanWatcher) Subscribe() <-chan struct{} {
	ch := make(chan struct{}, 1)
	w.subsMu.Lock()
	w.subs = append(w.subs, ch)
	w.subsMu.Unlock()
	return ch
}

// notify sends to all subscriber channels (non-blocking). When a channel's
// buffer is full the notification is coalesced (the subscriber will see one
// signal when it drains) and droppedNotifications is incremented.
func (w *KanbanWatcher) notify() {
	w.subsMu.Lock()
	defer w.subsMu.Unlock()
	for _, ch := range w.subs {
		select {
		case ch <- struct{}{}:
		default:
			w.droppedNotifications.Add(1)
		}
	}
}

// DroppedNotifications returns how many subscriber sends were coalesced because
// the consumer's buffer was full. Useful for diagnosing slow consumers.
func (w *KanbanWatcher) DroppedNotifications() int64 {
	if w == nil {
		return 0
	}
	return w.droppedNotifications.Load()
}

// setCountsAndStatusesAndNotify updates counts and the per-task status map atomically.
// When markSeedOK is true, seedOK is set to true inside the same lock so IsHealthy()
// never returns true while counts are still stale.
func (w *KanbanWatcher) setCountsAndStatusesAndNotify(running, blocked int, statuses map[string]string, markSeedOK bool) {
	w.mu.Lock()
	changed := w.running != running || w.blocked != blocked
	if markSeedOK {
		w.seedOK = true
	}
	w.running = running
	w.blocked = blocked
	w.taskStatuses = statuses
	w.mu.Unlock()
	if changed {
		w.notify()
	}
}

// Unsubscribe removes ch from the subscriber list. Call after the channel has
// fired to prevent dead channels from accumulating in w.subs indefinitely.
func (w *KanbanWatcher) Unsubscribe(ch <-chan struct{}) {
	w.subsMu.Lock()
	for i, c := range w.subs {
		if c == ch {
			w.subs = append(w.subs[:i], w.subs[i+1:]...)
			break
		}
	}
	w.subsMu.Unlock()
}

// reconnectLoop dials WebSocket, reads events, and reconnects on disconnect.
func (w *KanbanWatcher) reconnectLoop() {
	backoff := kanbanInitialBackoff
	for {
		select {
		case <-w.stopCh:
			return
		default:
		}

		established, err := w.runSession()
		if err == nil {
			// Clean disconnect (Stop called); exit without retrying.
			return
		}

		// Clear health so GetHermesKanbanCounts falls back to CLI polling
		// until the next successful seed — prevents serving stale watcher data.
		w.mu.Lock()
		w.seedOK = false
		w.mu.Unlock()

		// Reset backoff only when this session was fully established (past WS
		// dial). A failed seed or dial should keep accumulating backoff.
		if established {
			backoff = kanbanInitialBackoff
		}
		kanbanLog.Debug("kanban_watcher_disconnected",
			slog.String("error", err.Error()),
			slog.Duration("backoff", backoff),
		)

		// Exponential backoff before retry
		select {
		case <-w.stopCh:
			return
		case <-time.After(backoff):
		}
		backoff *= kanbanBackoffFactor
		if backoff > kanbanMaxBackoff {
			backoff = kanbanMaxBackoff
		}
	}
}

// runSession connects, seeds counts, reads events, returns on disconnect or error.
// established is true when the WebSocket connection was successfully opened;
// callers use this to decide whether to reset reconnect backoff.
func (w *KanbanWatcher) runSession() (established bool, err error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Stop context when watcher is stopped
	go func() {
		select {
		case <-w.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	// Seed initial counts and per-task statuses via HTTP board endpoint.
	running, blocked, statuses, err := w.seedCounts(ctx)
	if err != nil {
		return false, fmt.Errorf("seed counts: %w", err)
	}
	w.setCountsAndStatusesAndNotify(running, blocked, statuses, true)

	// Build WebSocket URL
	wsURL := w.buildWSURL()

	// Retrieve last event ID under lock for the query param
	w.mu.RLock()
	lastID := w.lastEventID
	w.mu.RUnlock()

	if lastID > 0 {
		wsURL = fmt.Sprintf("%s?since=%d", wsURL, lastID)
	}

	dialer := websocket.DefaultDialer
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return false, fmt.Errorf("websocket dial: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Bound frame size, install a read deadline, and refresh the deadline on
	// every pong. Without these, a half-open connection (NAT timeout, LB idle
	// reaper, power-cycled switch) silently wedges readEvents forever while
	// IsHealthy() keeps returning true — the worst failure class.
	conn.SetReadLimit(kanbanWSReadLimit)
	_ = conn.SetReadDeadline(time.Now().Add(kanbanReadTimeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(kanbanReadTimeout))
	})

	kanbanLog.Debug("kanban_watcher_connected", slog.String("url", wsURL))

	// Send pings on an idle interval so the pong handler keeps refreshing the
	// read deadline. Exits when readEvents returns or stopCh fires.
	pingDone := make(chan struct{})
	go func() {
		t := time.NewTicker(kanbanPingInterval)
		defer t.Stop()
		for {
			select {
			case <-pingDone:
				return
			case <-w.stopCh:
				return
			case <-t.C:
				deadline := time.Now().Add(10 * time.Second)
				if err := conn.WriteControl(websocket.PingMessage, nil, deadline); err != nil {
					return
				}
			}
		}
	}()

	readDone := make(chan error, 1)
	go func() {
		readDone <- w.readEvents(conn)
	}()

	select {
	case <-w.stopCh:
		close(pingDone)
		_ = conn.Close()
		return true, nil
	case err := <-readDone:
		close(pingDone)
		return true, err
	}
}

// seedCounts fetches the current board state via HTTP and returns running/blocked
// counts along with a per-task status map.
func (w *KanbanWatcher) seedCounts(ctx context.Context) (running, blocked int, statuses map[string]string, err error) {
	boardURL := w.buildHTTPURL() + "/api/plugins/kanban/board?include_archived=false"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, boardURL, nil)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := kanbanSeedClient.Do(req)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("http get board: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return 0, 0, nil, fmt.Errorf("board endpoint returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MB cap
	if err != nil {
		return 0, 0, nil, fmt.Errorf("read body: %w", err)
	}

	var board kanbanBoardResponse
	if err := json.Unmarshal(body, &board); err != nil {
		return 0, 0, nil, fmt.Errorf("unmarshal board: %w", err)
	}

	statuses = make(map[string]string)
	for _, t := range board.Tasks {
		switch t.Status {
		case "running", "claimed":
			running++
			if t.ID != "" {
				statuses[t.ID] = "running"
			}
		case "blocked":
			blocked++
			if t.ID != "" {
				statuses[t.ID] = "blocked"
			}
		}
	}
	return running, blocked, statuses, nil
}

// readEvents reads WebSocket messages and updates counts.
func (w *KanbanWatcher) readEvents(conn *websocket.Conn) error {
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read message: %w", err)
		}

		var evt kanbanEvent
		if err := json.Unmarshal(msg, &evt); err != nil {
			kanbanLog.Debug("kanban_event_unmarshal_failed",
				slog.String("error", err.Error()),
				slog.String("raw", string(msg)),
			)
			continue
		}

		w.applyEvent(evt)
	}
}

// applyEvent updates in-memory counts and per-task status based on the event kind.
func (w *KanbanWatcher) applyEvent(evt kanbanEvent) {
	w.mu.Lock()

	// Skip replayed/duplicate events to prevent count drift.
	// The ?since= query param reduces duplicates but does not eliminate them.
	if evt.ID > 0 && evt.ID <= w.lastEventID {
		w.mu.Unlock()
		return
	}

	running := w.running
	blocked := w.blocked

	// prev is the task's current tracked status (empty if unseen/terminal).
	prev := w.taskStatuses[evt.TaskID]

	switch evt.Kind {
	case "claimed":
		// Only count if the task wasn't already tracked as active — prevents
		// double-counting if a claimed event is delivered after a reconnect seed.
		if prev == "" && evt.TaskID != "" {
			running++
			w.taskStatuses[evt.TaskID] = "running"
		} else if evt.TaskID == "" {
			running++
		}
	case "blocked":
		if evt.TaskID != "" {
			switch prev {
			case "running":
				// Transition running→blocked: swap counters.
				if running > 0 {
					running--
				}
				blocked++
			case "blocked":
				// Already blocked — no-op on counts.
			default:
				// Newly blocked (unseen task).
				blocked++
			}
			w.taskStatuses[evt.TaskID] = "blocked"
		} else {
			blocked++
		}
	case "unblocked":
		if evt.TaskID != "" {
			switch prev {
			case "blocked":
				// Transition blocked→running: swap counters.
				if blocked > 0 {
					blocked--
				}
				running++
				w.taskStatuses[evt.TaskID] = "running"
			// "running" or "" — no prior blocked state to transition from; ignore.
			// An unseen task appearing as "unblocked" is a stale or out-of-order
			// event; the seed would have captured its current state already.
			}
		} else {
			// No task ID: swap counters only if we actually had a blocked task
			// to move. Without this guard, an "unblocked" event arriving when
			// blocked==0 would phantom-increment running.
			if blocked > 0 {
				blocked--
				running++
			}
		}
	case "reclaimed":
		// Task re-queued by Hermes for another agent — transitions back to running.
		if evt.TaskID != "" {
			switch prev {
			case "blocked":
				if blocked > 0 {
					blocked--
				}
				running++
			case "running":
				// Already tracked as running — no counter change needed.
			default:
				// Unseen task surfacing as reclaimed; count it as running.
				running++
			}
			w.taskStatuses[evt.TaskID] = "running"
		} else {
			running++
		}
	case "completed", "archived", "crashed", "timed_out":
		if evt.TaskID != "" {
			switch prev {
			case "running":
				if running > 0 {
					running--
				}
			case "blocked":
				if blocked > 0 {
					blocked--
				}
			}
			delete(w.taskStatuses, evt.TaskID)
		} else {
			// No task ID — best-effort decrement running.
			if running > 0 {
				running--
			}
		}
	}

	changed := w.running != running || w.blocked != blocked
	w.running = running
	w.blocked = blocked
	if evt.ID > w.lastEventID {
		w.lastEventID = evt.ID
	}
	w.mu.Unlock()

	if changed {
		w.notify()
	}
}

// buildWSURL converts the gateway base URL to a WebSocket events endpoint.
// Handles http://, https://, ws://, wss:// prefixes.
func (w *KanbanWatcher) buildWSURL() string {
	base := w.gatewayURL
	switch {
	case strings.HasPrefix(base, "https://"):
		base = "wss://" + strings.TrimPrefix(base, "https://")
	case strings.HasPrefix(base, "http://"):
		base = "ws://" + strings.TrimPrefix(base, "http://")
	}
	base = strings.TrimRight(base, "/")
	return base + "/api/plugins/kanban/events"
}

// buildHTTPURL converts the gateway base URL to an HTTP URL.
func (w *KanbanWatcher) buildHTTPURL() string {
	base := w.gatewayURL
	switch {
	case strings.HasPrefix(base, "wss://"):
		base = "https://" + strings.TrimPrefix(base, "wss://")
	case strings.HasPrefix(base, "ws://"):
		base = "http://" + strings.TrimPrefix(base, "ws://")
	}
	return strings.TrimRight(base, "/")
}
