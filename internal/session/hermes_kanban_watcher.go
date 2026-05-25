package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
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
// through a small state machine that mirrors Hermes's own event semantics.
//
// Concurrency model:
//
//   - All public methods are safe to call from any goroutine.
//   - Two locks: w.mu (RWMutex) guards count/status/cursor/cache fields;
//     w.subsMu (Mutex) guards the subscribers slice. They are NEVER held
//     simultaneously: notify() is always invoked after w.mu.Unlock() so a
//     future subscriber callback that reaches back into the watcher cannot
//     deadlock.
//   - Two goroutine sources: one long-lived pollLoop started by Start();
//     and at most one short-lived cache-refresh goroutine spawned by
//     maybeRefreshCache when Counts/TaskStatus is called on an unhealthy
//     watcher. ForceRefreshCache runs inline on the caller's goroutine and
//     does not participate in that bound — callers are expected not to
//     stampede (in practice, a single Bubble Tea Cmd).
//   - Start and Stop are idempotent. Stop does not wait for an in-flight
//     cache-refresh goroutine — that goroutine self-cancels within ~3s via
//     exec.CommandContext, and its final applyCacheResult is harmless
//     (subscribers nil, fields unread).
type KanbanWatcher struct {
	dbPath string

	mu             sync.RWMutex
	running        int
	blocked        int
	taskStatuses   map[string]taskStatus
	cursor         int64 // last-seen task_events.id
	sqliteHealthy  bool  // true while the SQLite poll is the authoritative source

	// CLI cache fallback — used by Counts/TaskStatus when sqliteHealthy is
	// false (kanban.db missing or unreadable). The cache writes the same
	// running/blocked/taskStatuses fields above; sqliteHealthy records which
	// source wrote them last so a late-arriving cache refresh cannot clobber
	// SQLite-authoritative values.
	cacheFetchedAt  time.Time
	cacheRefreshing bool

	stopCh    chan struct{}
	stopOnce  sync.Once
	startOnce sync.Once

	subsMu sync.Mutex
	subs   []chan struct{}

	droppedNotifications atomic.Int64

	// CLI-refresh error log-rate-limiting state. Atomic-only; no struct lock.
	// Each flag drives "log this class of error at most once per failure
	// streak"; reset on the next successful refresh in refreshCacheFromCLI.
	cliRefreshFailed         atomic.Bool // any-exec-failure streak active
	cliBinaryMissingLogged   atomic.Bool // "hermes not in PATH" logged once
}

var kanbanLog = logging.ForComponent("hermes-kanban")

// Three time intervals govern the watcher. They look similar (all "every N
// seconds") but live at different layers of the design:
//
//   - kanbanDBPollInterval (500ms): cadence of the SQLite tail in pollLoop.
//     This is the primary update mechanism; sub-second latency for badges.
//   - kanbanReseedInterval (5m): defense-in-depth full re-seed from the
//     `tasks` table inside pollLoop. Bounds any drift the event stream
//     might accumulate.
//   - kanbanCacheTTL (15s): freshness window for the CLI fallback cache,
//     active only when sqliteHealthy is false. Mirrors the UI-layer fallback
//     ticker (ui.kanbanCLITickInterval, also 15s); when the fallback is in
//     use both fire together.
const (
	kanbanDBPollInterval = 500 * time.Millisecond
	kanbanReseedInterval = 5 * time.Minute
	kanbanCacheTTL       = 15 * time.Second
)

// taskStatus is the in-memory representation of a task's lifecycle position
// from the agent-deck UI's perspective. Only two states are surfaced as
// badges; everything else (ready, done, archived, ...) collapses to unknown.
// Kept unexported so the public API (KanbanWatcher.TaskStatus) keeps returning
// string via String(); callers don't see the type.
type taskStatus int8

const (
	statusUnknown taskStatus = iota
	statusRunning
	statusBlocked
)

// String returns the legacy stringly-typed value, preserving the public
// TaskStatus(id string) string contract: "running", "blocked", or "".
func (s taskStatus) String() string {
	switch s {
	case statusRunning:
		return "running"
	case statusBlocked:
		return "blocked"
	default:
		return ""
	}
}

// parseDBStatus maps a `tasks.status` column value to a taskStatus.
// Both "running" and "claimed" map to statusRunning — claimed means a worker
// has the lock and is executing, which we surface as "running" to the user.
// Other statuses (ready, done, archived, ...) are not counted as active.
func parseDBStatus(s string) taskStatus {
	switch s {
	case "running", "claimed":
		return statusRunning
	case "blocked":
		return statusBlocked
	default:
		return statusUnknown
	}
}

// eventKind is the typed representation of `task_events.kind`. Centralizing
// the string→enum translation at the SQLite boundary means `applyEvent`'s
// switch can't silently miss-spell a kind, and unknown kinds become a typed
// no-op (kindIgnored) instead of an implicit default branch.
type eventKind int8

const (
	kindIgnored   eventKind = iota // unknown/uninteresting kind (assigned, commented, ...)
	kindClaimed
	kindBlocked
	kindUnblocked
	kindReclaimed
	kindCompleted
	kindArchived
	kindCrashed
	kindTimedOut
	kindGaveUp
)

// terminalKinds enumerates the event kinds that retire a task from the
// running/blocked counters. Shared between applyEvent and tests so any
// future Hermes addition (e.g. a hypothetical "killed") gets added in one
// place and the test coverage follows automatically.
var terminalKinds = []eventKind{
	kindCompleted, kindArchived, kindCrashed, kindTimedOut, kindGaveUp,
}

// String returns the same string Hermes writes to task_events.kind. Used by
// tests for subtest naming and by any future debug-format need.
func (k eventKind) String() string {
	switch k {
	case kindClaimed:
		return "claimed"
	case kindBlocked:
		return "blocked"
	case kindUnblocked:
		return "unblocked"
	case kindReclaimed:
		return "reclaimed"
	case kindCompleted:
		return "completed"
	case kindArchived:
		return "archived"
	case kindCrashed:
		return "crashed"
	case kindTimedOut:
		return "timed_out"
	case kindGaveUp:
		return "gave_up"
	default:
		return "ignored"
	}
}

// parseEventKind maps a `task_events.kind` column value to an eventKind.
// Returns kindIgnored for any kind we don't handle — these become no-ops in
// applyEvent rather than triggering a missing-case bug.
func parseEventKind(s string) eventKind {
	switch s {
	case "claimed":
		return kindClaimed
	case "blocked":
		return kindBlocked
	case "unblocked":
		return kindUnblocked
	case "reclaimed":
		return kindReclaimed
	case "completed":
		return kindCompleted
	case "archived":
		return kindArchived
	case "crashed":
		return kindCrashed
	case "timed_out":
		return kindTimedOut
	case "gave_up":
		return kindGaveUp
	default:
		return kindIgnored
	}
}

// kanbanEvent mirrors the relevant columns of the task_events row, post-
// parsing. Kind is the typed enum so applyEvent's switch is exhaustive at
// the compiler level (modulo kindIgnored as the safe default).
type kanbanEvent struct {
	ID     int64
	Kind   eventKind
	TaskID string
}

// HermesKanbanDBPath returns the standard path to Hermes's kanban.db.
// The path is computed deterministically from GetHermesConfigDir; the file
// is not required to exist (the KanbanWatcher tolerates a missing file).
func HermesKanbanDBPath() string {
	return filepath.Join(GetHermesConfigDir(), "kanban.db")
}

// NewKanbanWatcher constructs a watcher rooted at the given SQLite file.
// The file does not need to exist yet; Start will fall into a retry loop and
// IsHealthy() will report false until the first successful seed.
func NewKanbanWatcher(dbPath string) *KanbanWatcher {
	return &KanbanWatcher{
		dbPath:       dbPath,
		taskStatuses: make(map[string]taskStatus),
		stopCh:       make(chan struct{}),
	}
}

// Start launches the poll loop in a background goroutine. Idempotent: only
// the first call has effect.
func (w *KanbanWatcher) Start() {
	w.startOnce.Do(func() {
		go w.pollLoop()
	})
}

// Stop signals the poll loop to exit and closes every subscriber channel so
// any goroutine ranging over a returned subscription exits naturally.
// Idempotent. Stop does NOT wait for an in-flight cache-refresh goroutine —
// that goroutine self-cancels within ~3s via exec.CommandContext, and its
// final state write after Stop returns is harmless (subscribers nil, fields
// unread by anyone still active). A KanbanWatcher is single-use; after Stop,
// construct a new one rather than reusing the stopped instance.
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

// Counts returns the current running and blocked task counts. When the SQLite
// poll is healthy, values are at most kanbanDBPollInterval (500ms) stale.
// Otherwise the CLI cache is consulted; if the cache is older than
// kanbanCacheTTL (15s) a background `hermes kanban list --json` refresh is
// scheduled. Note: that refresh updates the cache for the NEXT call — the
// values returned by this call are still whatever was already in memory.
// Subscribe to Subscribe() to be notified when an async refresh completes.
func (w *KanbanWatcher) Counts() (running, blocked int) {
	w.mu.RLock()
	if w.sqliteHealthy {
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
	if w.sqliteHealthy {
		defer w.mu.RUnlock()
		return w.taskStatuses[id].String()
	}
	w.mu.RUnlock()
	w.maybeRefreshCache()
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.taskStatuses[id].String()
}

// IsHealthy reports whether sub-second SQLite updates are currently flowing.
// Use this ONLY to decide whether to also run a redundant data-source (e.g.
// a CLI poll for an external dashboard); NEVER use it to decide whether the
// watcher's data is usable — Counts/TaskStatus always return the best-
// available values regardless of this flag.
func (w *KanbanWatcher) IsHealthy() bool {
	if w == nil {
		return false
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.sqliteHealthy
}

// Subscribe returns a buffered channel that receives a signal whenever counts
// change. Capacity 1; slow consumers see coalesced updates but never block
// the watcher.
//
// Lifecycle: each call returns a NEW channel. Stop() closes every subscriber
// channel, so a consumer ranging over the returned channel will exit cleanly
// when Stop is called. A caller that drops the returned channel without
// calling Unsubscribe leaks the channel until Stop. After Stop, subsequent
// Subscribe calls return channels that will never receive nor close.
func (w *KanbanWatcher) Subscribe() <-chan struct{} {
	ch := make(chan struct{}, 1)
	w.subsMu.Lock()
	w.subs = append(w.subs, ch)
	w.subsMu.Unlock()
	return ch
}

// Unsubscribe removes ch from the subscriber list. Safe to call with a
// channel that was never subscribed (no-op). Note: each Subscribe returns a
// distinct channel — passing the wrong one (e.g. the previous channel after
// a re-Subscribe) silently does nothing.
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
//
// A panic inside this goroutine would silently freeze badges forever (no more
// notifications would ever fire). The defer-recover ensures the operator at
// least sees an Error log if it happens; the loop will then exit, but a
// dead-but-loud watcher is strictly better than a dead-and-silent one.
func (w *KanbanWatcher) pollLoop() {
	defer func() {
		if r := recover(); r != nil {
			kanbanLog.Error("kanban_pollloop_panic",
				slog.Any("panic", r),
				slog.String("db_path", w.dbPath),
			)
		}
	}()

	lastSeed := time.Time{}
	lastDroppedLogged := int64(0)
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
				kanbanLog.Debug("kanban_db_seed_failed",
					slog.String("error", err.Error()),
					slog.String("db_path", w.dbPath),
				)
				// Wait before retry; back off a little but stay responsive.
				select {
				case <-w.stopCh:
					return
				case <-time.After(2 * time.Second):
				}
				continue
			}
			lastSeed = time.Now()

			// Once per reseed (every 5min) emit a notice if subscribers have
			// been dropping notifications — symptom of a stalled UI consumer.
			if dropped := w.droppedNotifications.Load(); dropped > lastDroppedLogged {
				kanbanLog.Info("kanban_dropped_notifications",
					slog.Int64("total", dropped),
					slog.Int64("since_last_log", dropped-lastDroppedLogged),
				)
				lastDroppedLogged = dropped
			}
		}

		// Poll for new events.
		if err := w.fetchNewEvents(); err != nil {
			kanbanLog.Debug("kanban_db_poll_failed",
				slog.String("error", err.Error()),
				slog.String("db_path", w.dbPath),
			)
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

	statuses := make(map[string]taskStatus)
	var running, blocked int

	rows, err := db.QueryContext(ctx, `SELECT id, status FROM tasks`)
	if err != nil {
		return fmt.Errorf("query tasks: %w", err)
	}
	for rows.Next() {
		var id, statusStr string
		if err := rows.Scan(&id, &statusStr); err != nil {
			rows.Close()
			return fmt.Errorf("scan task row: %w", err)
		}
		switch parseDBStatus(statusStr) {
		case statusRunning:
			running++
			if id != "" {
				statuses[id] = statusRunning
			}
		case statusBlocked:
			blocked++
			if id != "" {
				statuses[id] = statusBlocked
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
// healthy, and notifies subscribers if counts changed. Logs an Info line at
// each unhealthy→healthy transition so an operator can grep "kanban_sqlite_
// recovered" to find when live updates resumed.
func (w *KanbanWatcher) applySeed(running, blocked int, statuses map[string]taskStatus, cursor int64) {
	w.mu.Lock()
	changed := w.running != running || w.blocked != blocked
	recovered := !w.sqliteHealthy
	w.running = running
	w.blocked = blocked
	w.taskStatuses = statuses
	w.cursor = cursor
	w.sqliteHealthy = true
	w.mu.Unlock()
	if recovered {
		kanbanLog.Info("kanban_sqlite_recovered",
			slog.String("db_path", w.dbPath),
			slog.Int("running", running),
			slog.Int("blocked", blocked),
		)
	}
	if changed {
		w.notify()
	}
}

// markUnhealthy clears sqliteHealthy so Counts/TaskStatus fall through to the
// CLI cache. Existing count fields are NOT cleared — the UI keeps showing the
// last-known values until the cache (or a successful re-seed) overwrites them.
// Logs a Warn line at each healthy→unhealthy transition; subsequent failures
// while unhealthy are not re-logged.
func (w *KanbanWatcher) markUnhealthy() {
	w.mu.Lock()
	degraded := w.sqliteHealthy
	w.sqliteHealthy = false
	w.mu.Unlock()
	if degraded {
		kanbanLog.Warn("kanban_sqlite_degraded",
			slog.String("db_path", w.dbPath),
		)
	}
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
		var (
			evt     kanbanEvent
			kindStr string
		)
		if err := rows.Scan(&evt.ID, &evt.TaskID, &kindStr); err != nil {
			return fmt.Errorf("scan event: %w", err)
		}
		evt.Kind = parseEventKind(kindStr)
		w.applyEvent(evt)
	}
	return rows.Err()
}

// applyEvent updates in-memory counts and per-task status based on the event
// kind. Uses w.taskStatuses[evt.TaskID] (prev) to drive state-machine
// transitions: e.g. a "blocked" event for a task whose prev is "running"
// decrements running and increments blocked, whereas the same event for an
// unseen task only increments blocked.
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
	case kindClaimed:
		if prev == statusUnknown && evt.TaskID != "" {
			running++
			w.taskStatuses[evt.TaskID] = statusRunning
		}
	case kindBlocked:
		if evt.TaskID != "" {
			switch prev {
			case statusRunning:
				if running > 0 {
					running--
				}
				blocked++
			case statusBlocked:
				// Already blocked — no-op.
			default:
				blocked++
			}
			w.taskStatuses[evt.TaskID] = statusBlocked
		}
	case kindUnblocked:
		if evt.TaskID != "" && prev == statusBlocked {
			if blocked > 0 {
				blocked--
			}
			running++
			w.taskStatuses[evt.TaskID] = statusRunning
		}
		// statusRunning / statusUnknown — ignore; out-of-order or stale event.
	case kindReclaimed:
		if evt.TaskID != "" {
			switch prev {
			case statusBlocked:
				if blocked > 0 {
					blocked--
				}
				running++
			case statusRunning:
				// Already tracked as running — no counter change.
			default:
				running++
			}
			w.taskStatuses[evt.TaskID] = statusRunning
		}
	case kindCompleted, kindArchived, kindCrashed, kindTimedOut, kindGaveUp:
		if evt.TaskID != "" {
			switch prev {
			case statusRunning:
				if running > 0 {
					running--
				}
			case statusBlocked:
				if blocked > 0 {
					blocked--
				}
			}
			delete(w.taskStatuses, evt.TaskID)
		}
	case kindIgnored:
		// Unknown/uninteresting kind (assigned, commented, promoted, ...) —
		// no counter change. Explicit case for exhaustiveness; the compiler
		// catches missing cases the next time we add a kind to the enum.
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
// whichever source last wrote them owns the current value. sqliteHealthy
// records who wrote last so a late-arriving cache refresh cannot clobber
// SQLite-authoritative values.
// ----------------------------------------------------------------------------

// ForceRefreshCache refreshes the CLI fallback cache immediately, bypassing
// the TTL and the in-flight guard that maybeRefreshCache uses. The refresh
// runs in-line on the caller's goroutine. Callers are responsible for not
// stampeding — in practice this is invoked from a single Bubble Tea Cmd at
// kanbanCLITickInterval, so concurrent invocations don't occur in normal use.
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
// and applies it via applyCacheResult. Errors are mostly silent: a failed
// CLI call leaves the previous cached values in place and the next call
// will retry. However:
//   - "hermes binary not found in PATH" is logged Info ONCE per atomic flag
//     (users who have not installed Hermes get a single startup mention,
//     not log spam).
//   - Other exec failures (non-zero exit, timeout, killed) are logged Warn
//     ONCE per consecutive-failure streak, reset on the next success — so an
//     incident produces a single Warn at onset, not one per 15-second tick.
//   - JSON unmarshal failures are logged Warn with a payload preview; these
//     indicate hermes CLI output drift and need investigation.
func (w *KanbanWatcher) refreshCacheFromCLI() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "hermes", "kanban", "list", "--json").Output()
	if err != nil {
		w.logCLIRefreshError(err)
		return
	}
	// On success, reset the failure-streak flags so future failures log again.
	w.cliRefreshFailed.Store(false)
	var tasks []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &tasks); err != nil {
		preview := string(out)
		if len(preview) > 200 {
			preview = preview[:200]
		}
		kanbanLog.Warn("kanban_cli_unmarshal_failed",
			slog.String("error", err.Error()),
			slog.String("payload_preview", preview),
		)
		return
	}
	var running, blocked int
	statuses := make(map[string]taskStatus)
	for _, t := range tasks {
		switch parseDBStatus(t.Status) {
		case statusRunning:
			running++
			if t.ID != "" {
				statuses[t.ID] = statusRunning
			}
		case statusBlocked:
			blocked++
			if t.ID != "" {
				statuses[t.ID] = statusBlocked
			}
		}
	}
	w.applyCacheResult(running, blocked, statuses)
}

// applyCacheResult installs a fresh cache snapshot under w.mu and notifies
// subscribers if counts changed. Exposed as a separate method so tests can
// exercise the cache path without forking a subprocess.
//
// Note: this does NOT set sqliteHealthy. The cache is the unhealthy-fallback
// path; sqliteHealthy remains the property of the SQLite poll only.
func (w *KanbanWatcher) applyCacheResult(running, blocked int, statuses map[string]taskStatus) {
	w.mu.Lock()
	changed := w.running != running || w.blocked != blocked
	// Only overwrite when the SQLite poll is NOT healthy. If SQLite became
	// healthy between when refreshCacheFromCLI started and now, its values
	// are authoritative and we should not clobber them.
	wasHealthy := w.sqliteHealthy
	if !wasHealthy {
		w.running = running
		w.blocked = blocked
		w.taskStatuses = statuses
	}
	w.cacheFetchedAt = time.Now()
	w.mu.Unlock()
	if changed && !wasHealthy {
		w.notify()
	}
}

// logCLIRefreshError logs a CLI subprocess failure at the right severity
// without spamming. "hermes not in PATH" is the common case for users who
// haven't installed Hermes — that's an Info-once condition. Other failures
// (non-zero exit, timeout, killed) are operational incidents and log Warn-
// once per failure streak.
func (w *KanbanWatcher) logCLIRefreshError(err error) {
	if errors.Is(err, exec.ErrNotFound) {
		if !w.cliBinaryMissingLogged.Swap(true) {
			kanbanLog.Info("kanban_cli_not_installed",
				slog.String("hint", "install hermes for CLI fallback when kanban.db is unreadable"),
			)
		}
		return
	}
	if !w.cliRefreshFailed.Swap(true) {
		kanbanLog.Warn("kanban_cli_refresh_failed",
			slog.String("error", err.Error()),
		)
	}
}
