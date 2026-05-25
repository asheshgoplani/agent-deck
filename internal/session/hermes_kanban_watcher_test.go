package session

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// makeTestDB creates a fresh SQLite file in a temp dir with the Hermes
// kanban.db schema. Returns the path and an open *sql.DB the caller can use
// to seed rows. The DB is closed automatically when the test ends.
func makeTestDB(t *testing.T) (path string, db *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "kanban.db")
	d, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	_, err = d.Exec(`
        CREATE TABLE tasks (
            id TEXT PRIMARY KEY,
            title TEXT NOT NULL,
            status TEXT NOT NULL,
            created_at INTEGER NOT NULL,
            workspace_kind TEXT NOT NULL DEFAULT 'scratch'
        );
        CREATE TABLE task_events (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            task_id TEXT NOT NULL,
            run_id INTEGER,
            kind TEXT NOT NULL,
            payload TEXT,
            created_at INTEGER NOT NULL
        );
    `)
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	return path, d
}

// insertTask inserts a single tasks row.
func insertTask(t *testing.T, db *sql.DB, id, status string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO tasks (id, title, status, created_at) VALUES (?, ?, ?, ?)`,
		id, "task "+id, status, time.Now().UnixMilli(),
	)
	if err != nil {
		t.Fatalf("insert task: %v", err)
	}
}

// insertEvent inserts a task_events row and returns its rowid.
func insertEvent(t *testing.T, db *sql.DB, taskID, kind string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO task_events (task_id, kind, payload, created_at) VALUES (?, ?, ?, ?)`,
		taskID, kind, "{}", time.Now().UnixMilli(),
	)
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	return id
}

// waitForCounts polls Counts() until both equal the desired values or the
// timeout elapses. Returns true on success.
func waitForCounts(w *KanbanWatcher, wantRunning, wantBlocked int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		r, b := w.Counts()
		if r == wantRunning && b == wantBlocked {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// waitForHealthy polls IsHealthy() until true or the timeout elapses.
func waitForHealthy(w *KanbanWatcher, want bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if w.IsHealthy() == want {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// ----------------------------------------------------------------------------
// Construction / lifecycle
// ----------------------------------------------------------------------------

func TestKanbanWatcher_CountsStartAtZero(t *testing.T) {
	w := NewKanbanWatcher(filepath.Join(t.TempDir(), "does-not-exist.db"))
	r, b := w.Counts()
	if r != 0 || b != 0 {
		t.Fatalf("counts = (%d,%d), want (0,0)", r, b)
	}
}

func TestKanbanWatcher_IsHealthyFalseBeforeStart(t *testing.T) {
	w := NewKanbanWatcher(filepath.Join(t.TempDir(), "does-not-exist.db"))
	if w.IsHealthy() {
		t.Fatal("IsHealthy() = true before Start; want false")
	}
}

func TestKanbanWatcher_StopIsIdempotent(t *testing.T) {
	w := NewKanbanWatcher(filepath.Join(t.TempDir(), "does-not-exist.db"))
	w.Stop()
	w.Stop()
	w.Stop()
}

func TestKanbanWatcher_StartIsIdempotent(t *testing.T) {
	path, _ := makeTestDB(t)
	w := NewKanbanWatcher(path)
	w.Start()
	w.Start()
	w.Start()
	defer w.Stop()
	if !waitForHealthy(w, true, 2*time.Second) {
		t.Fatal("watcher never became healthy")
	}
}

// ----------------------------------------------------------------------------
// applyEvent — state machine unit tests
// ----------------------------------------------------------------------------

func TestApplyEvent_ClaimedNewTask(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 1, Kind: kindClaimed, TaskID: "T1"})
	r, b := w.Counts()
	if r != 1 || b != 0 {
		t.Fatalf("counts = (%d,%d), want (1,0)", r, b)
	}
	if got := w.TaskStatus("T1"); got != "running" {
		t.Fatalf("TaskStatus(T1) = %q, want running", got)
	}
}

func TestApplyEvent_ClaimedAlreadyRunning(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 1, Kind: kindClaimed, TaskID: "T1"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: kindClaimed, TaskID: "T1"})
	r, b := w.Counts()
	if r != 1 || b != 0 {
		t.Fatalf("counts = (%d,%d), want (1,0) — no double increment", r, b)
	}
}

func TestApplyEvent_BlockedFromRunning(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 1, Kind: kindClaimed, TaskID: "T1"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: kindBlocked, TaskID: "T1"})
	r, b := w.Counts()
	if r != 0 || b != 1 {
		t.Fatalf("counts = (%d,%d), want (0,1)", r, b)
	}
	if got := w.TaskStatus("T1"); got != "blocked" {
		t.Fatalf("TaskStatus(T1) = %q, want blocked", got)
	}
}

func TestApplyEvent_BlockedUnseenTask(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 1, Kind: kindBlocked, TaskID: "T1"})
	r, b := w.Counts()
	if r != 0 || b != 1 {
		t.Fatalf("counts = (%d,%d), want (0,1)", r, b)
	}
}

func TestApplyEvent_BlockedAlreadyBlocked(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 1, Kind: kindBlocked, TaskID: "T1"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: kindBlocked, TaskID: "T1"})
	r, b := w.Counts()
	if r != 0 || b != 1 {
		t.Fatalf("counts = (%d,%d), want (0,1) — no double increment", r, b)
	}
}

func TestApplyEvent_UnblockedFromBlocked(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 1, Kind: kindBlocked, TaskID: "T1"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: kindUnblocked, TaskID: "T1"})
	r, b := w.Counts()
	if r != 1 || b != 0 {
		t.Fatalf("counts = (%d,%d), want (1,0)", r, b)
	}
	if got := w.TaskStatus("T1"); got != "running" {
		t.Fatalf("TaskStatus(T1) = %q, want running", got)
	}
}

func TestApplyEvent_UnblockedFromRunningIsNoop(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 1, Kind: kindClaimed, TaskID: "T1"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: kindUnblocked, TaskID: "T1"})
	r, b := w.Counts()
	if r != 1 || b != 0 {
		t.Fatalf("counts = (%d,%d), want (1,0) — stale unblocked is no-op", r, b)
	}
}

func TestApplyEvent_UnblockedUnseenIsNoop(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 1, Kind: kindUnblocked, TaskID: "T1"})
	r, b := w.Counts()
	if r != 0 || b != 0 {
		t.Fatalf("counts = (%d,%d), want (0,0) — unseen unblocked is no-op", r, b)
	}
}

func TestApplyEvent_ReclaimedFromBlocked(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 1, Kind: kindBlocked, TaskID: "T1"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: kindReclaimed, TaskID: "T1"})
	r, b := w.Counts()
	if r != 1 || b != 0 {
		t.Fatalf("counts = (%d,%d), want (1,0)", r, b)
	}
}

func TestApplyEvent_ReclaimedFromRunningIsNoop(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 1, Kind: kindClaimed, TaskID: "T1"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: kindReclaimed, TaskID: "T1"})
	r, b := w.Counts()
	if r != 1 || b != 0 {
		t.Fatalf("counts = (%d,%d), want (1,0) — already-running reclaim is no-op", r, b)
	}
}

func TestApplyEvent_ReclaimedUnseenIncrementsRunning(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 1, Kind: kindReclaimed, TaskID: "T1"})
	r, b := w.Counts()
	if r != 1 || b != 0 {
		t.Fatalf("counts = (%d,%d), want (1,0)", r, b)
	}
}

func TestApplyEvent_TerminalKindsDecrementRunning(t *testing.T) {
	// Drives off the production-side terminalKinds list so a future addition
	// (e.g. kindKilled) gets coverage automatically.
	for _, kind := range terminalKinds {
		t.Run(kind.String(), func(t *testing.T) {
			w := NewKanbanWatcher("ignored")
			w.applyEvent(kanbanEvent{ID: 1, Kind: kindClaimed, TaskID: "T1"})
			w.applyEvent(kanbanEvent{ID: 2, Kind: kind, TaskID: "T1"})
			r, b := w.Counts()
			if r != 0 || b != 0 {
				t.Fatalf("counts = (%d,%d), want (0,0)", r, b)
			}
			if got := w.TaskStatus("T1"); got != "" {
				t.Fatalf("TaskStatus(T1) = %q, want empty", got)
			}
		})
	}
}

func TestApplyEvent_CompletedFromBlockedDecrementsBlocked(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 1, Kind: kindBlocked, TaskID: "T1"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: kindCompleted, TaskID: "T1"})
	r, b := w.Counts()
	if r != 0 || b != 0 {
		t.Fatalf("counts = (%d,%d), want (0,0)", r, b)
	}
}

func TestApplyEvent_CompletedUnseenIsNoop(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 1, Kind: kindCompleted, TaskID: "T1"})
	r, b := w.Counts()
	if r != 0 || b != 0 {
		t.Fatalf("counts = (%d,%d), want (0,0)", r, b)
	}
}

func TestApplyEvent_DuplicateEventIgnored(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 5, Kind: kindClaimed, TaskID: "T1"})
	// Same id replayed — cursor already at 5, so this must be ignored even
	// though logically the kind would otherwise be a no-op anyway. Replay
	// with a count-changing kind to make the assertion sharp.
	w.applyEvent(kanbanEvent{ID: 5, Kind: kindBlocked, TaskID: "T1"})
	r, b := w.Counts()
	if r != 1 || b != 0 {
		t.Fatalf("counts = (%d,%d), want (1,0) — duplicate id must be ignored", r, b)
	}
}

func TestApplyEvent_LowerIDIgnored(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 10, Kind: kindClaimed, TaskID: "T1"})
	w.applyEvent(kanbanEvent{ID: 3, Kind: kindBlocked, TaskID: "T1"})
	r, b := w.Counts()
	if r != 1 || b != 0 {
		t.Fatalf("counts = (%d,%d), want (1,0) — out-of-order id below cursor must be ignored", r, b)
	}
}

func TestApplyEvent_AdvancesCursor(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 7, Kind: kindClaimed, TaskID: "T1"})
	w.mu.RLock()
	got := w.cursor
	w.mu.RUnlock()
	if got != 7 {
		t.Fatalf("cursor = %d, want 7", got)
	}
}

// ----------------------------------------------------------------------------
// Counts never go negative under pathological input
// ----------------------------------------------------------------------------

func TestApplyEvent_CountsNeverNegative(t *testing.T) {
	w := NewKanbanWatcher("ignored")

	// Terminate tasks that were never seen — must not push counters negative.
	w.applyEvent(kanbanEvent{ID: 1, Kind: kindCompleted, TaskID: "ghost-1"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: kindArchived, TaskID: "ghost-2"})
	w.applyEvent(kanbanEvent{ID: 3, Kind: kindCrashed, TaskID: "ghost-3"})
	w.applyEvent(kanbanEvent{ID: 4, Kind: kindTimedOut, TaskID: "ghost-4"})
	w.applyEvent(kanbanEvent{ID: 5, Kind: kindGaveUp, TaskID: "ghost-5"})
	w.applyEvent(kanbanEvent{ID: 6, Kind: kindUnblocked, TaskID: "ghost-6"})

	r, b := w.Counts()
	if r != 0 {
		t.Fatalf("running = %d, want 0", r)
	}
	if b != 0 {
		t.Fatalf("blocked = %d, want 0", b)
	}
}

// ----------------------------------------------------------------------------
// Subscribe / Unsubscribe behavior
// ----------------------------------------------------------------------------

func TestSubscribe_ReceivesNotificationOnChange(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	ch := w.Subscribe()

	w.applyEvent(kanbanEvent{ID: 1, Kind: kindClaimed, TaskID: "T1"})

	select {
	case <-ch:
		// good
	case <-time.After(200 * time.Millisecond):
		t.Fatal("subscriber did not receive notification within 200ms")
	}
}

func TestUnsubscribe_StopsDelivery(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	ch := w.Subscribe()
	w.Unsubscribe(ch)

	w.applyEvent(kanbanEvent{ID: 1, Kind: kindClaimed, TaskID: "T1"})

	select {
	case <-ch:
		t.Fatal("unsubscribed channel still received notification")
	default:
		// good
	}
}

func TestSubscribe_SecondSubscriberStillReceives(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	chA := w.Subscribe()
	chB := w.Subscribe()
	w.Unsubscribe(chA)

	w.applyEvent(kanbanEvent{ID: 1, Kind: kindClaimed, TaskID: "T1"})

	select {
	case <-chB:
		// good
	case <-time.After(200 * time.Millisecond):
		t.Fatal("remaining subscriber did not receive notification")
	}

	// chA should still not see anything.
	select {
	case <-chA:
		t.Fatal("unsubscribed channel A unexpectedly received notification")
	default:
	}
}

func TestSubscribe_NoNotifyWhenCountsUnchanged(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	// Pre-claim so the second claimed event is a no-op.
	w.applyEvent(kanbanEvent{ID: 1, Kind: kindClaimed, TaskID: "T1"})

	ch := w.Subscribe()
	w.applyEvent(kanbanEvent{ID: 2, Kind: kindClaimed, TaskID: "T1"}) // no change

	select {
	case <-ch:
		t.Fatal("subscriber received notification despite no count change")
	case <-time.After(100 * time.Millisecond):
		// good
	}
}

func TestDroppedNotifications_IncrementsWhenSubscriberFull(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	ch := w.Subscribe()

	// First event fills the buffer (cap 1).
	w.applyEvent(kanbanEvent{ID: 1, Kind: kindClaimed, TaskID: "T1"})
	// Second count-changing event finds the channel already full → dropped.
	w.applyEvent(kanbanEvent{ID: 2, Kind: kindBlocked, TaskID: "T1"})
	// Third count-changing event also dropped (we never drained).
	w.applyEvent(kanbanEvent{ID: 3, Kind: kindUnblocked, TaskID: "T1"})

	if got := w.DroppedNotifications(); got < 2 {
		t.Fatalf("DroppedNotifications() = %d, want >= 2", got)
	}
	// Drain so the test goroutine doesn't leak references; nothing more to assert.
	select {
	case <-ch:
	default:
	}
}

// ----------------------------------------------------------------------------
// Integration test: real SQLite file driven by NewKanbanWatcher.Start()
// ----------------------------------------------------------------------------

func TestKanbanWatcher_Integration_SeedsAndTailsEvents(t *testing.T) {
	path, db := makeTestDB(t)

	// Seed: T1 running, T2 claimed (also counts as running), T3 blocked, T4 done (ignored).
	insertTask(t, db, "T1", "running")
	insertTask(t, db, "T2", "claimed")
	insertTask(t, db, "T3", "blocked")
	insertTask(t, db, "T4", "done")

	w := NewKanbanWatcher(path)

	// Subscribe BEFORE any insert to avoid races; this channel should fire when
	// the new task_events row is observed.
	sub := w.Subscribe()

	w.Start()
	defer w.Stop()

	if !waitForHealthy(w, true, 2*time.Second) {
		t.Fatal("watcher never became healthy")
	}

	// Initial counts: T1 + T2 running, T3 blocked.
	if !waitForCounts(w, 2, 1, 2*time.Second) {
		r, b := w.Counts()
		t.Fatalf("initial counts = (%d,%d), want (2,1)", r, b)
	}
	if got := w.TaskStatus("T1"); got != "running" {
		t.Errorf("TaskStatus(T1) = %q, want running", got)
	}
	if got := w.TaskStatus("T3"); got != "blocked" {
		t.Errorf("TaskStatus(T3) = %q, want blocked", got)
	}

	// The seed itself might have notified once if counts changed from zero
	// (they did). Drain any pending signal so the next assertion is sharp.
	select {
	case <-sub:
	case <-time.After(50 * time.Millisecond):
	}

	// Append a "blocked" event for T1 (which is currently running).
	insertEvent(t, db, "T1", "blocked")

	// Expected: running 2→1, blocked 1→2.
	if !waitForCounts(w, 1, 2, 2*time.Second) {
		r, b := w.Counts()
		t.Fatalf("post-event counts = (%d,%d), want (1,2)", r, b)
	}

	// Subscriber should have been signaled.
	select {
	case <-sub:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber never received notification for new event")
	}

	if got := w.TaskStatus("T1"); got != "blocked" {
		t.Errorf("after event, TaskStatus(T1) = %q, want blocked", got)
	}
}

func TestKanbanWatcher_Integration_StopClosesSubscribers(t *testing.T) {
	path, _ := makeTestDB(t)

	w := NewKanbanWatcher(path)
	w.Start()
	if !waitForHealthy(w, true, 2*time.Second) {
		w.Stop()
		t.Fatal("watcher never became healthy")
	}
	sub := w.Subscribe()

	w.Stop()

	// Reading from a closed channel returns immediately with the zero value
	// and ok=false. We just need the read to unblock within a reasonable time.
	select {
	case _, ok := <-sub:
		if ok {
			// A pre-close notification is fine, but the channel must then close;
			// read once more to confirm.
			select {
			case _, ok2 := <-sub:
				if ok2 {
					t.Fatal("subscriber channel did not close after Stop()")
				}
			case <-time.After(time.Second):
				t.Fatal("subscriber channel did not close after Stop()")
			}
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber channel did not close after Stop()")
	}
}

// ----------------------------------------------------------------------------
// IsHealthy lifecycle
// ----------------------------------------------------------------------------

func TestKanbanWatcher_IsHealthy_FalseWhenDBMissing(t *testing.T) {
	// Point at a path that doesn't exist; seed will fail every time.
	path := filepath.Join(t.TempDir(), "missing.db")
	w := NewKanbanWatcher(path)
	w.Start()
	defer w.Stop()

	// Give the poll loop a moment to attempt and fail to seed.
	time.Sleep(300 * time.Millisecond)
	if w.IsHealthy() {
		t.Fatal("IsHealthy() = true when db file is missing; want false")
	}
}

func TestKanbanWatcher_IsHealthy_TrueAfterSeed(t *testing.T) {
	path, _ := makeTestDB(t)
	w := NewKanbanWatcher(path)
	w.Start()
	defer w.Stop()

	if !waitForHealthy(w, true, 2*time.Second) {
		t.Fatal("watcher never became healthy with a valid db")
	}
}

func TestKanbanWatcher_Seed_DirectCall(t *testing.T) {
	path, db := makeTestDB(t)
	insertTask(t, db, "T1", "running")
	insertTask(t, db, "T2", "blocked")
	// Pre-existing event so the cursor advances at seed.
	cursorID := insertEvent(t, db, "T1", "claimed")

	w := NewKanbanWatcher(path)
	if err := w.seed(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	r, b := w.Counts()
	if r != 1 || b != 1 {
		t.Fatalf("counts after seed = (%d,%d), want (1,1)", r, b)
	}
	if !w.IsHealthy() {
		t.Fatal("IsHealthy() = false after successful seed; want true")
	}

	// Cursor should equal the max task_events.id observed.
	w.mu.RLock()
	gotCursor := w.cursor
	w.mu.RUnlock()
	if gotCursor != cursorID {
		t.Fatalf("cursor after seed = %d, want %d", gotCursor, cursorID)
	}
}

func TestKanbanWatcher_Seed_FailsWhenFileMissing(t *testing.T) {
	w := NewKanbanWatcher(filepath.Join(t.TempDir(), "nope.db"))
	if err := w.seed(); err == nil {
		t.Fatal("seed() returned nil error for missing file; want error")
	}
	if w.IsHealthy() {
		t.Fatal("IsHealthy() = true after failed seed; want false")
	}
}

// ----------------------------------------------------------------------------
// CLI cache fallback (used when SQLite poll is unhealthy)
// ----------------------------------------------------------------------------

// TestKanbanWatcher_FallbackReturnsCachedCounts verifies that when the SQLite
// poll is unhealthy, Counts/TaskStatus return values written by applyCacheResult.
func TestKanbanWatcher_FallbackReturnsCachedCounts(t *testing.T) {
	w := NewKanbanWatcher(filepath.Join(t.TempDir(), "no.db"))
	// SQLite-unhealthy state (sqliteHealthy=false is the zero value).
	w.applyCacheResult(3, 2, map[string]taskStatus{
		"T_run":   statusRunning,
		"T_block": statusBlocked,
	})
	r, b := w.Counts()
	if r != 3 || b != 2 {
		t.Fatalf("Counts after cache result = (%d,%d), want (3,2)", r, b)
	}
	if got := w.TaskStatus("T_run"); got != "running" {
		t.Fatalf("TaskStatus(T_run) = %q, want running", got)
	}
	if got := w.TaskStatus("T_block"); got != "blocked" {
		t.Fatalf("TaskStatus(T_block) = %q, want blocked", got)
	}
}

// TestKanbanWatcher_IsHealthyFalseEvenWithCachedData pins the contract that
// IsHealthy refers to the SQLite poll specifically — having cached data does
// NOT make us healthy.
func TestKanbanWatcher_IsHealthyFalseEvenWithCachedData(t *testing.T) {
	w := NewKanbanWatcher(filepath.Join(t.TempDir(), "no.db"))
	w.applyCacheResult(5, 0, map[string]taskStatus{"T1": statusRunning})
	if w.IsHealthy() {
		t.Fatal("IsHealthy() = true after cache populate; want false (cache != healthy)")
	}
}

// TestKanbanWatcher_SQLiteValuesOverrideCache verifies that once the SQLite
// poll becomes healthy, applyCacheResult does NOT clobber its values.
// This guards against the race where a cache refresh started while unhealthy
// completes after the SQLite poll has succeeded.
func TestKanbanWatcher_SQLiteValuesOverrideCache(t *testing.T) {
	w := NewKanbanWatcher(filepath.Join(t.TempDir(), "no.db"))
	// Pretend SQLite poll succeeded with running=10.
	w.applySeed(10, 0, map[string]taskStatus{"T_sqlite": statusRunning}, 0)
	if !w.IsHealthy() {
		t.Fatal("expected IsHealthy=true after applySeed")
	}
	// Late-arriving cache refresh must not clobber.
	w.applyCacheResult(1, 1, map[string]taskStatus{"T_cache": statusRunning})
	r, b := w.Counts()
	if r != 10 || b != 0 {
		t.Fatalf("Counts after late cache = (%d,%d), want (10,0) — SQLite must win", r, b)
	}
	if got := w.TaskStatus("T_sqlite"); got != "running" {
		t.Fatalf("TaskStatus(T_sqlite) = %q, want running", got)
	}
}

// TestKanbanWatcher_MaybeRefreshCache_KicksOffWhenStale verifies the
// background-refresh trigger fires when the cache is older than TTL.
func TestKanbanWatcher_MaybeRefreshCache_KicksOffWhenStale(t *testing.T) {
	w := NewKanbanWatcher(filepath.Join(t.TempDir(), "no.db"))
	// Force the cache to look very stale.
	w.mu.Lock()
	w.cacheFetchedAt = time.Now().Add(-2 * kanbanCacheTTL)
	w.mu.Unlock()
	w.maybeRefreshCache()
	// maybeRefreshCache sets cacheRefreshing=true and spawns a goroutine.
	// Verify the in-flight flag was set (it may have already cleared if the
	// subprocess fails fast on a system without hermes — in that case the
	// goroutine completed and reset it). Either the flag is true now, or
	// cacheFetchedAt has been updated by a completed refresh.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		w.mu.RLock()
		refreshing := w.cacheRefreshing
		fetched := w.cacheFetchedAt
		w.mu.RUnlock()
		// A refresh either is in flight, or completed and updated fetchedAt.
		if refreshing || time.Since(fetched) < time.Second {
			return // success
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("maybeRefreshCache did not appear to schedule or complete a refresh")
}

// TestKanbanWatcher_MaybeRefreshCache_NoopWhenFresh verifies no refresh is
// scheduled when the cache is within TTL.
func TestKanbanWatcher_MaybeRefreshCache_NoopWhenFresh(t *testing.T) {
	w := NewKanbanWatcher(filepath.Join(t.TempDir(), "no.db"))
	// Fresh cache.
	w.mu.Lock()
	w.cacheFetchedAt = time.Now()
	w.mu.Unlock()
	w.maybeRefreshCache()
	w.mu.RLock()
	refreshing := w.cacheRefreshing
	w.mu.RUnlock()
	if refreshing {
		t.Fatal("maybeRefreshCache set cacheRefreshing=true while cache was fresh")
	}
}

// TestKanbanWatcher_MaybeRefreshCache_NoConcurrent verifies a second call
// is a no-op while a refresh is in flight.
func TestKanbanWatcher_MaybeRefreshCache_NoConcurrent(t *testing.T) {
	w := NewKanbanWatcher(filepath.Join(t.TempDir(), "no.db"))
	w.mu.Lock()
	w.cacheFetchedAt = time.Now().Add(-2 * kanbanCacheTTL)
	w.cacheRefreshing = true // pretend one is already in flight
	w.mu.Unlock()

	w.maybeRefreshCache()
	// The flag should remain true — we did not spawn a second goroutine that
	// would later reset it. This is asserted by the fact that the value
	// remains true synchronously after the call.
	w.mu.RLock()
	refreshing := w.cacheRefreshing
	w.mu.RUnlock()
	if !refreshing {
		t.Fatal("maybeRefreshCache cleared cacheRefreshing while it was already true")
	}
}

// TestKanbanWatcher_ApplyCacheResult_NotifiesSubscribers verifies the cache
// path participates in the same notification system as the SQLite path.
func TestKanbanWatcher_ApplyCacheResult_NotifiesSubscribers(t *testing.T) {
	w := NewKanbanWatcher(filepath.Join(t.TempDir(), "no.db"))
	ch := w.Subscribe()

	w.applyCacheResult(1, 0, map[string]taskStatus{"T1": statusRunning})

	select {
	case <-ch:
		// ok
	case <-time.After(200 * time.Millisecond):
		t.Fatal("subscriber did not receive notification on cache result")
	}
}

// TestKanbanWatcher_ApplyCacheResult_NoNotifyWhenSeedOK verifies that when
// SQLite is healthy, a late cache result does NOT notify (counts didn't
// actually change from the subscriber's perspective).
func TestKanbanWatcher_ApplyCacheResult_NoNotifyWhenSeedOK(t *testing.T) {
	w := NewKanbanWatcher(filepath.Join(t.TempDir(), "no.db"))
	w.applySeed(5, 0, map[string]taskStatus{"T_sqlite": statusRunning}, 0)
	ch := w.Subscribe()

	// Late cache refresh with different values — must be ignored.
	w.applyCacheResult(99, 99, map[string]taskStatus{"T_cache": statusRunning})

	select {
	case <-ch:
		t.Fatal("subscriber received notification while SQLite was authoritative")
	case <-time.After(100 * time.Millisecond):
		// ok
	}
}

// ----------------------------------------------------------------------------
// Round 5 elegance pass: regression tests for newly-fixed bugs
// ----------------------------------------------------------------------------

// TestApplySeed_CursorMonotonic verifies that a reseed with a lower MAX(id)
// (e.g. after task_events was rotated/compacted) cannot rewind the cursor.
// Without this guard, fetchNewEvents would re-apply events we already
// processed and double-count counters.
func TestApplySeed_CursorMonotonic(t *testing.T) {
	w := NewKanbanWatcher("ignored")

	w.applySeed(0, 0, map[string]taskStatus{}, 100)
	w.mu.RLock()
	if w.cursor != 100 {
		w.mu.RUnlock()
		t.Fatalf("cursor after first seed = %d, want 100", w.cursor)
	}
	w.mu.RUnlock()

	// Simulate the events table being rotated/cleared: MAX(id) drops to 50.
	w.applySeed(0, 0, map[string]taskStatus{}, 50)
	w.mu.RLock()
	if w.cursor != 100 {
		w.mu.RUnlock()
		t.Fatalf("cursor after seed with lower id = %d, want 100 (must not regress)", w.cursor)
	}
	w.mu.RUnlock()

	// A higher value continues to advance.
	w.applySeed(0, 0, map[string]taskStatus{}, 200)
	w.mu.RLock()
	if w.cursor != 200 {
		w.mu.RUnlock()
		t.Fatalf("cursor after seed with higher id = %d, want 200", w.cursor)
	}
	w.mu.RUnlock()
}

// TestSubscribe_AfterStopReturnsClosedChannel verifies the documented
// behavior: post-Stop subscribers see a closed channel rather than blocking
// forever. A consumer ranging over it exits cleanly.
func TestSubscribe_AfterStopReturnsClosedChannel(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.Stop()

	ch := w.Subscribe()

	// The channel must be closed; receiving returns zero value with ok=false.
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected closed channel; received with ok=true")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Subscribe after Stop blocked instead of returning closed channel")
	}
}

// TestParseEventKind_UnknownLogsOnceAndIgnored verifies that:
//   - Truly-unknown event kinds parse to kindIgnored.
//   - knownIgnoredKinds (assigned, commented, ...) parse to kindIgnored
//     without logging.
func TestParseEventKind_UnknownIgnored(t *testing.T) {
	// Known typed kinds.
	for _, c := range []struct {
		in   string
		want eventKind
	}{
		{"claimed", kindClaimed},
		{"blocked", kindBlocked},
		{"gave_up", kindGaveUp},
	} {
		if got := parseEventKind(c.in); got != c.want {
			t.Errorf("parseEventKind(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	// Known-ignored kinds.
	for _, k := range []string{"assigned", "commented", "promoted", "scheduled", "spawned"} {
		if got := parseEventKind(k); got != kindIgnored {
			t.Errorf("parseEventKind(%q) = %v, want kindIgnored", k, got)
		}
	}
	// Genuinely unknown kind: should parse to kindIgnored.
	if got := parseEventKind("future_kind_v2"); got != kindIgnored {
		t.Errorf("parseEventKind(unknown) = %v, want kindIgnored", got)
	}
	// Empty string: also kindIgnored (no log).
	if got := parseEventKind(""); got != kindIgnored {
		t.Errorf("parseEventKind(\"\") = %v, want kindIgnored", got)
	}
}

// TestFetchFailStreak_AtomicCounter verifies the streak counter increments
// and resets correctly. The full pollLoop logging behavior is covered by
// inspection — this just locks down the counter mechanics.
func TestFetchFailStreak_AtomicCounter(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	for i := int32(1); i <= 3; i++ {
		if got := w.fetchFailStreak.Add(1); got != i {
			t.Fatalf("after %d failures, fetchFailStreak = %d, want %d", i, got, i)
		}
	}
	if old := w.fetchFailStreak.Swap(0); old != 3 {
		t.Fatalf("swap reset = %d, want 3", old)
	}
	if cur := w.fetchFailStreak.Load(); cur != 0 {
		t.Fatalf("after reset = %d, want 0", cur)
	}
}

// TestApplyEvent_TaskTableCap verifies that when taskStatuses hits the cap,
// new claimed events neither insert into the map nor increment counters —
// preventing the counter from drifting up forever without a corresponding
// retire-on-completed.
func TestApplyEvent_TaskTableCap(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	// Fill the map to one below the cap.
	for i := 0; i < kanbanMaxTrackedTasks; i++ {
		w.taskStatuses[fmt.Sprintf("filler-%d", i)] = statusRunning
	}
	w.running = kanbanMaxTrackedTasks

	// One more claim at the cap: must NOT increment.
	w.applyEvent(kanbanEvent{ID: 1, Kind: kindClaimed, TaskID: "over-cap-task"})

	if r, _ := w.Counts(); r != kanbanMaxTrackedTasks {
		t.Fatalf("running after over-cap claim = %d, want %d (no increment when full)", r, kanbanMaxTrackedTasks)
	}
	if _, present := w.taskStatuses["over-cap-task"]; present {
		t.Fatal("over-cap task was inserted into map; cap not enforced")
	}
}

// TestMarkUnhealthy_PreservesLastKnownCounts verifies the primitive that
// pollLoop's panic-recovery handler depends on: after markUnhealthy flips
// sqliteHealthy=false, Counts continues returning the last-known values
// (driven by the SQLite path before the panic), so the UI shows stale-but-
// known instead of zero.
func TestMarkUnhealthy_PreservesLastKnownCounts(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	// Pretend a previous seed succeeded.
	w.applySeed(5, 0, map[string]taskStatus{"T": statusRunning}, 0)
	if !w.IsHealthy() {
		t.Fatal("setup: expected IsHealthy=true after applySeed")
	}

	w.markUnhealthy()

	if w.IsHealthy() {
		t.Fatal("after markUnhealthy: expected IsHealthy=false")
	}
	if r, _ := w.Counts(); r != 5 {
		t.Fatalf("Counts after markUnhealthy = %d, want 5 (last known)", r)
	}
}

// TestPollLoopRecover_MarksUnhealthy verifies pollLoop's panic-recovery
// handler (extracted as a method so it's directly testable). Calls the
// helper with a synthesized panic value and asserts the watcher transitions
// to unhealthy. If anyone deletes the pollLoopRecover call from pollLoop's
// defer, that's a code-review-only catch — but if the helper's body itself
// regresses, this test will fail.
func TestPollLoopRecover_MarksUnhealthy(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applySeed(7, 1, map[string]taskStatus{"T": statusRunning, "B": statusBlocked}, 0)
	if !w.IsHealthy() {
		t.Fatal("setup: expected IsHealthy=true after applySeed")
	}

	w.pollLoopRecover("simulated panic value")

	if w.IsHealthy() {
		t.Fatal("pollLoopRecover did not clear sqliteHealthy")
	}
	// Counts still reflect last-known seed values.
	if r, b := w.Counts(); r != 7 || b != 1 {
		t.Fatalf("Counts after pollLoopRecover = (%d,%d), want (7,1)", r, b)
	}
}
