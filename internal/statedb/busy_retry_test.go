package statedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

var fakeBusyErr = errors.New("database is locked (SQLITE_BUSY)")

func TestWithBusyRetry_SucceedsFirstTry(t *testing.T) {
	calls := 0
	err := withBusyRetry(func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestWithBusyRetry_RetriesOnBusy(t *testing.T) {
	calls := 0
	err := withBusyRetry(func() error {
		calls++
		if calls < 3 {
			return fakeBusyErr
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil after retries, got %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestWithBusyRetry_DoesNotRetryNonBusy(t *testing.T) {
	other := errors.New("constraint failed: NOT NULL")
	calls := 0
	err := withBusyRetry(func() error {
		calls++
		return other
	})
	if !errors.Is(err, other) {
		t.Errorf("expected %v, got %v", other, err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call (no retry on non-busy), got %d", calls)
	}
}

func TestWithBusyRetry_ExhaustsRetries(t *testing.T) {
	calls := 0
	err := withBusyRetry(func() error {
		calls++
		return fakeBusyErr
	})
	if err == nil {
		t.Fatalf("expected error after exhausted retries, got nil")
	}
	if !isSQLiteBusy(err) {
		t.Errorf("expected busy error, got %v", err)
	}
	if calls != 5 {
		t.Errorf("expected 5 attempts, got %d", calls)
	}
}

// openSingleConnDB opens a StateDB configured with a single connection and
// busy_timeout=0 so SQLITE_BUSY surfaces immediately when another connection
// holds a write lock. This makes app-level retry behavior deterministic.
func openSingleConnDB(t *testing.T, dbPath string) *StateDB {
	t.Helper()
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db.db.SetMaxOpenConns(1)
	if _, err := db.db.Exec("PRAGMA busy_timeout=0"); err != nil {
		t.Fatalf("set busy_timeout=0: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// briefWriteLock acquires a SQLite write lock on dbPath via BEGIN IMMEDIATE,
// holds it for d, then releases. Returns a channel closed after release.
func briefWriteLock(t *testing.T, dbPath string, d time.Duration) <-chan struct{} {
	t.Helper()
	holder, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("holder open: %v", err)
	}
	if _, err := holder.Exec("PRAGMA busy_timeout=5000"); err != nil {
		holder.Close()
		t.Fatalf("holder pragma: %v", err)
	}
	ctx := context.Background()
	conn, err := holder.Conn(ctx)
	if err != nil {
		holder.Close()
		t.Fatalf("holder conn: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		conn.Close()
		holder.Close()
		t.Fatalf("BEGIN IMMEDIATE: %v", err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(d)
		_, _ = conn.ExecContext(ctx, "ROLLBACK")
		_ = conn.Close()
		_ = holder.Close()
		close(released)
	}()
	return released
}

// TestWriteStatus_RetriesOnBusy proves WriteStatus tolerates a transient
// SQLITE_BUSY by waiting for the lock to release. Without app-level retry
// (the bug today: transition_daemon.go:149 calls WriteStatus and the helper
// has no retry), this fails immediately when busy_timeout is exhausted.
func TestWriteStatus_RetriesOnBusy(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db := openSingleConnDB(t, dbPath)
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := db.SaveInstance(&InstanceRow{
		ID: "inst-1", Title: "t", ProjectPath: "/tmp", GroupPath: "g",
		Tool: "shell", Status: "idle", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveInstance: %v", err)
	}

	lockHeld := briefWriteLock(t, dbPath, 80*time.Millisecond)
	if err := db.WriteStatus("inst-1", "running", "shell"); err != nil {
		t.Fatalf("WriteStatus under contention: %v", err)
	}
	<-lockHeld
}

// TestUpdateWatcherEventRoutedTo_RetriesOnBusy is the parity test against
// SaveWatcherEvent (which already retries). Pre-fix the asymmetry surfaces
// as transient errors during routing under conductor crash-restart cycles.
func TestUpdateWatcherEventRoutedTo_RetriesOnBusy(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db := openSingleConnDB(t, dbPath)
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := db.SaveWatcher(&WatcherRow{
		ID: "w1", Name: "wt", Type: "webhook",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveWatcher: %v", err)
	}
	if _, err := db.SaveWatcherEvent("w1", "k1", "s", "subj", "", "", 100); err != nil {
		t.Fatalf("SaveWatcherEvent: %v", err)
	}

	lockHeld := briefWriteLock(t, dbPath, 80*time.Millisecond)
	if err := db.UpdateWatcherEventRoutedTo("w1", "k1", "conductor-x", "sess-1"); err != nil {
		t.Fatalf("UpdateWatcherEventRoutedTo under contention: %v", err)
	}
	<-lockHeld
}

// TestPruneWatcherEvents_RetriesOnBusy covers the same retry contract for
// the prune path. Prune is currently called as `_ = pruneWatcherEvents(...)`
// from SaveWatcherEvent so a swallowed BUSY would let the table grow.
func TestPruneWatcherEvents_RetriesOnBusy(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db := openSingleConnDB(t, dbPath)
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := db.SaveWatcher(&WatcherRow{
		ID: "w1", Name: "wt", Type: "webhook",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveWatcher: %v", err)
	}
	for i := 0; i < 6; i++ {
		if _, err := db.SaveWatcherEvent("w1", fmt.Sprintf("k%d", i), "s", "sub", "", "", 1000); err != nil {
			t.Fatalf("seed event: %v", err)
		}
	}

	lockHeld := briefWriteLock(t, dbPath, 80*time.Millisecond)
	if err := db.pruneWatcherEvents("w1", 3); err != nil {
		t.Fatalf("pruneWatcherEvents under contention: %v", err)
	}
	<-lockHeld

	var n int
	if err := db.db.QueryRow("SELECT COUNT(*) FROM watcher_events WHERE watcher_id='w1'").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 3 {
		t.Errorf("expected 3 rows after prune, got %d", n)
	}
}
