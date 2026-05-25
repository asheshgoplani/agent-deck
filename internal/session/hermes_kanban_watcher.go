package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// KanbanWatcher reads Hermes Kanban state directly from ~/.hermes/kanban.db,
// the same SQLite file the Hermes CLI, dashboard plugin, and gateway notifier
// all read from. It does NOT speak HTTP or WebSocket — the dashboard plugin's
// own /events WebSocket is itself just a poll-and-relay over this same table.
// Reading directly removes a network hop, an authentication dependency, and a
// process dependency (the dashboard does not need to be running).
//
// The watcher seeds counts from the `tasks` table and then tails the
// append-only `task_events` table on a short interval, applying each event
// through the same state machine the prior WebSocket watcher used.
type KanbanWatcher struct {
	dbPath string

	mu           sync.RWMutex
	running      int
	blocked      int
	taskStatuses map[string]string
	cursor       int64 // last-seen task_events.id
	seedOK       bool

	// CLI cache fallback — used by Counts/TaskStatus when seedOK is false
	// (the SQLite path is unhealthy, e.g. kanban.db missing or unreadable).
	// The same running/blocked/taskStatuses fields above hold cached values
	// in this mode; cacheFetchedAt records when they were last refreshed and
	// cacheRefreshing guards against multiple concurrent subprocess refreshes.
	cacheFetchedAt  time.Time
	cacheRefreshing bool

	stopCh    chan struct{}
	stopOnce  sync.Once
	startOnce sync.Once

	subsMu sync.Mutex
	subs   []chan struct{}

	droppedNotifications atomic.Int64
}

var kanbanLog = logging.ForComponent("hermes-kanban")

const (
	// kanbanDBPollInterval determines the maximum latency between a Hermes
	// state change and an agent-deck badge update. 500ms gives sub-second UX
	// while keeping the SQLite read cost negligible (one indexed range query).
	kanbanDBPollInterval = 500 * time.Millisecond

	// kanbanReseedInterval is the maximum time between full re-seeds from the
	// `tasks` table. Re-seeding bounds drift from missed or out-of-order events
	// (defense in depth — the state machine should already converge).
	kanbanReseedInterval = 5 * time.Minute

	// kanbanCacheTTL is how stale the CLI fallback cache may be before
	// Counts/TaskStatus kicks off a background refresh. Only relevant when
	// the SQLite poll is unhealthy.
	kanbanCacheTTL = 15 * time.Second
)

// kanbanEvent mirrors the relevant columns of the task_events row.
type kanbanEvent struct {
	ID     int64
	Kind   string
	TaskID string
}

// NewKanbanWatcher constructs a watcher rooted at the given SQLite file.
// The file does not need to exist yet; Start will fall into a retry loop and
// IsHealthy() will report false until the first successful seed.
func NewKanbanWatcher(dbPath string) *KanbanWatcher {
	return &KanbanWatcher{
		dbPath:       dbPath,
		taskStatuses: make(map[string]string),
		stopCh:       make(chan struct{}),
	}
}

// Start launches the poll loop in a background goroutine. Idempotent.
func (w *KanbanWatcher) Start() {
	w.startOnce.Do(func() {
		go w.pollLoop()
	})
}

// Stop signals the poll loop to exit and closes all subscriber channels so
// goroutines blocked on the subscription return. Idempotent.
func (w *KanbanWatcher) Stop() {
	w.stopOnce.Do(func() {
		close(w.stopCh)
		w.subsMu.Lock()
		for _, ch := range w.subs {
			close(ch)
		}
		w.subs = nil
		w.subsMu.Unlock()
	})
}

// Counts returns the current running and blocked task counts. When the
// SQLite poll is healthy, returns its values. Otherwise falls through to
// the CLI cache (`hermes kanban list --json`), kicking off a background
// refresh if the cache is stale.
func (w *KanbanWatcher) Counts() (running, blocked int) {
	w.mu.RLock()
	if w.seedOK {
		r, b := w.running, w.blocked
		w.mu.RUnlock()
		return r, b
	}
	w.mu.RUnlock()
	w.maybeRefreshCache()
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.running, w.blocked
}

// TaskStatus returns "running", "blocked", or "" (terminal / unknown) for the
// given task ID. Follows the same SQLite-then-cache priority as Counts.
func (w *KanbanWatcher) TaskStatus(id string) string {
	if id == "" {
		return ""
	}
	w.mu.RLock()
	if w.seedOK {
		defer w.mu.RUnlock()
		if w.taskStatuses == nil {
			return ""
		}
		return w.taskStatuses[id]
	}
	w.mu.RUnlock()
	w.maybeRefreshCache()
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.taskStatuses == nil {
		return ""
	}
	return w.taskStatuses[id]
}

// IsHealthy reports whether the watcher has successfully seeded from the
// kanban.db at least once. Callers should fall back to CLI polling when this
// returns false.
func (w *KanbanWatcher) IsHealthy() bool {
	if w == nil {
		return false
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.seedOK
}

// Subscribe returns a buffered channel that receives a signal whenever counts
// change. Capacity 1; slow consumers see coalesced updates but never block
// the watcher. Call Unsubscribe to release the channel when finished.
func (w *KanbanWatcher) Subscribe() <-chan struct{} {
	ch := make(chan struct{}, 1)
	w.subsMu.Lock()
	w.subs = append(w.subs, ch)
	w.subsMu.Unlock()
	return ch
}

// Unsubscribe removes ch from the subscriber list. Safe to call even if ch
// was never subscribed (no-op).
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

// DroppedNotifications returns how many subscriber sends were coalesced
// because the consumer's buffer was full.
func (w *KanbanWatcher) DroppedNotifications() int64 {
	if w == nil {
		return 0
	}
	return w.droppedNotifications.Load()
}

// notify signals every subscriber. Non-blocking: if a channel's buffer is
// full, the notification is dropped (coalesced) and droppedNotifications
// increments.
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

// pollLoop is the main goroutine. It seeds from the tasks table, then on each
// tick fetches new events from task_events. It re-seeds every kanbanReseedInterval
// to bound any drift the state machine might accumulate.
func (w *KanbanWatcher) pollLoop() {
	lastSeed := time.Time{}
	for {
		select {
		case <-w.stopCh:
			return
		default:
		}

		// (Re)seed when we've never seeded, or the reseed interval elapsed.
		if time.Since(lastSeed) > kanbanReseedInterval {
			if err := w.seed(); err != nil {
				w.markUnhealthy()
				kanbanLog.Debug("kanban_db_seed_failed", slog.String("error", err.Error()))
				// Wait before retry; back off a little but stay responsive.
				select {
				case <-w.stopCh:
					return
				case <-time.After(2 * time.Second):
				}
				continue
			}
			lastSeed = time.Now()
		}

		// Poll for new events.
		if err := w.fetchNewEvents(); err != nil {
			kanbanLog.Debug("kanban_db_poll_failed", slog.String("error", err.Error()))
			// Don't mark unhealthy on a transient poll error — the next seed
			// will either succeed (resync) or fail loudly.
		}

		select {
		case <-w.stopCh:
			return
		case <-time.After(kanbanDBPollInterval):
		}
	}
}

// openDB opens the SQLite file read-only with a short busy timeout. WAL mode
// is set on the file by whoever writes to it (Hermes); opening read-only does
// not require us to set it. The query string keeps the connection lightweight.
func (w *KanbanWatcher) openDB() (*sql.DB, error) {
	if _, err := os.Stat(w.dbPath); err != nil {
		return nil, fmt.Errorf("kanban.db not present: %w", err)
	}
	// mode=ro: read-only open; doesn't create the file.
	// _pragma=busy_timeout: wait briefly if the writer is mid-transaction.
	dsn := "file:" + w.dbPath + "?mode=ro&_pragma=busy_timeout(2000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open kanban.db: %w", err)
	}
	return db, nil
}

// seed reads the current task snapshot and the high-water-mark from
// task_events.id, replacing in-memory state in one atomic update.
func (w *KanbanWatcher) seed() error {
	db, err := w.openDB()
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	statuses := make(map[string]string)
	var running, blocked int

	rows, err := db.QueryContext(ctx, `SELECT id, status FROM tasks`)
	if err != nil {
		return fmt.Errorf("query tasks: %w", err)
	}
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			rows.Close()
			return fmt.Errorf("scan task row: %w", err)
		}
		switch status {
		case "running", "claimed":
			running++
			if id != "" {
				statuses[id] = "running"
			}
		case "blocked":
			blocked++
			if id != "" {
				statuses[id] = "blocked"
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate tasks: %w", err)
	}
	rows.Close()

	var cursor int64
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(id), 0) FROM task_events`).Scan(&cursor); err != nil {
		return fmt.Errorf("query high-water mark: %w", err)
	}

	w.applySeed(running, blocked, statuses, cursor)
	return nil
}

// applySeed installs the seeded state under a single lock, marks the watcher
// healthy, and notifies subscribers if counts changed.
func (w *KanbanWatcher) applySeed(running, blocked int, statuses map[string]string, cursor int64) {
	w.mu.Lock()
	changed := w.running != running || w.blocked != blocked
	w.running = running
	w.blocked = blocked
	w.taskStatuses = statuses
	w.cursor = cursor
	w.seedOK = true
	w.mu.Unlock()
	if changed {
		w.notify()
	}
}

// markUnhealthy clears seedOK so IsHealthy() returns false and the UI falls
// back to the CLI poll. Existing counts remain so the UI shows stale-but-known
// values rather than zero.
func (w *KanbanWatcher) markUnhealthy() {
	w.mu.Lock()
	w.seedOK = false
	w.mu.Unlock()
}

// fetchNewEvents pulls task_events with id > cursor and applies each through
// applyEvent. Cursor advances inside applyEvent.
func (w *KanbanWatcher) fetchNewEvents() error {
	w.mu.RLock()
	cursor := w.cursor
	w.mu.RUnlock()

	db, err := w.openDB()
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	rows, err := db.QueryContext(ctx,
		`SELECT id, task_id, kind FROM task_events WHERE id > ? ORDER BY id ASC LIMIT 1000`,
		cursor)
	if err != nil {
		return fmt.Errorf("query events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var evt kanbanEvent
		if err := rows.Scan(&evt.ID, &evt.TaskID, &evt.Kind); err != nil {
			return fmt.Errorf("scan event: %w", err)
		}
		w.applyEvent(evt)
	}
	return rows.Err()
}

// applyEvent updates in-memory counts and per-task status based on the event
// kind. Mirrors the state machine the prior WebSocket watcher used; the
// transition rules are identical because the events are identical (same
// task_events table, same `kind` strings).
func (w *KanbanWatcher) applyEvent(evt kanbanEvent) {
	w.mu.Lock()

	// Guard against re-application on the cold path (test code or a
	// future re-seed-during-poll race).
	if evt.ID > 0 && evt.ID <= w.cursor {
		w.mu.Unlock()
		return
	}

	running := w.running
	blocked := w.blocked
	prev := w.taskStatuses[evt.TaskID]

	switch evt.Kind {
	case "claimed":
		if prev == "" && evt.TaskID != "" {
			running++
			w.taskStatuses[evt.TaskID] = "running"
		}
	case "blocked":
		if evt.TaskID != "" {
			switch prev {
			case "running":
				if running > 0 {
					running--
				}
				blocked++
			case "blocked":
				// Already blocked — no-op.
			default:
				blocked++
			}
			w.taskStatuses[evt.TaskID] = "blocked"
		}
	case "unblocked":
		if evt.TaskID != "" && prev == "blocked" {
			if blocked > 0 {
				blocked--
			}
			running++
			w.taskStatuses[evt.TaskID] = "running"
		}
		// "running" / unseen — ignore; out-of-order or stale event.
	case "reclaimed":
		if evt.TaskID != "" {
			switch prev {
			case "blocked":
				if blocked > 0 {
					blocked--
				}
				running++
			case "running":
				// Already tracked as running — no counter change.
			default:
				running++
			}
			w.taskStatuses[evt.TaskID] = "running"
		}
	case "completed", "archived", "crashed", "timed_out", "gave_up":
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
		}
	}

	changed := w.running != running || w.blocked != blocked
	w.running = running
	w.blocked = blocked
	if evt.ID > w.cursor {
		w.cursor = evt.ID
	}
	w.mu.Unlock()

	if changed {
		w.notify()
	}
}

// ----------------------------------------------------------------------------
// CLI cache fallback
//
// When the SQLite poll is unhealthy (kanban.db missing / unreadable), Counts
// and TaskStatus fall through to a stale-while-revalidate cache populated by
// invoking `hermes kanban list --json` as a subprocess. The cache and the
// SQLite-poll share the same in-memory fields (running, blocked, taskStatuses);
// whichever source last wrote them owns the current value. seedOK distinguishes
// who wrote last.
// ----------------------------------------------------------------------------

// ForceRefreshCache refreshes the CLI fallback cache immediately, bypassing
// the TTL. Safe to call from a Bubble Tea Cmd goroutine; the refresh runs
// in-line on the caller's goroutine.
func (w *KanbanWatcher) ForceRefreshCache() {
	w.refreshCacheFromCLI()
}

// maybeRefreshCache kicks off a background refresh iff the cache is stale
// and no refresh is currently in flight. Returns immediately; cached values
// remain readable through w.mu while the refresh runs.
func (w *KanbanWatcher) maybeRefreshCache() {
	w.mu.Lock()
	stale := time.Since(w.cacheFetchedAt) >= kanbanCacheTTL
	if !stale || w.cacheRefreshing {
		w.mu.Unlock()
		return
	}
	w.cacheRefreshing = true
	w.mu.Unlock()

	go func() {
		w.refreshCacheFromCLI()
		w.mu.Lock()
		w.cacheRefreshing = false
		w.mu.Unlock()
	}()
}

// refreshCacheFromCLI runs `hermes kanban list --json`, parses the response,
// and applies it via applyCacheResult. Errors are silent: a failed CLI call
// (hermes not installed, command fails) leaves the previous cached values
// in place and the next call will retry.
func (w *KanbanWatcher) refreshCacheFromCLI() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "hermes", "kanban", "list", "--json").Output()
	if err != nil {
		return
	}
	var tasks []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &tasks); err != nil {
		return
	}
	var running, blocked int
	statuses := make(map[string]string)
	for _, t := range tasks {
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
	w.applyCacheResult(running, blocked, statuses)
}

// applyCacheResult installs a fresh cache snapshot under w.mu and notifies
// subscribers if counts changed. Exposed as a separate method so tests can
// exercise the cache path without forking a subprocess.
//
// Note: this does NOT set seedOK. The cache is the unhealthy-fallback path;
// seedOK remains the property of the SQLite poll only.
func (w *KanbanWatcher) applyCacheResult(running, blocked int, statuses map[string]string) {
	w.mu.Lock()
	changed := w.running != running || w.blocked != blocked
	// Only overwrite when the SQLite poll is NOT healthy. If SQLite became
	// healthy between when refreshCacheFromCLI started and now, its values
	// are authoritative and we should not clobber them.
	if !w.seedOK {
		w.running = running
		w.blocked = blocked
		w.taskStatuses = statuses
	}
	w.cacheFetchedAt = time.Now()
	w.mu.Unlock()
	if changed && !w.seedOKLocked() {
		w.notify()
	}
}

// seedOKLocked is a tiny helper that re-reads seedOK under a brief RLock —
// used by applyCacheResult to decide whether to notify after dropping the
// write lock. (We can't hold the write lock across notify because notify
// takes subsMu and could in principle interact poorly with future changes.)
func (w *KanbanWatcher) seedOKLocked() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.seedOK
}
