package ui

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/stretchr/testify/require"
)

func newTestDB(t *testing.T) *statedb.StateDB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := statedb.Open(dbPath)
	require.NoError(t, err)
	require.NoError(t, db.Migrate())
	t.Cleanup(func() { db.Close() })
	return db
}

func TestNewStorageWatcher(t *testing.T) {
	db := newTestDB(t)
	watcher, err := NewStorageWatcher(db)
	require.NoError(t, err)
	require.NotNil(t, watcher)
	defer watcher.Close()
}

func TestStorageWatcher_DetectsChanges(t *testing.T) {
	db := newTestDB(t)
	watcher, err := NewStorageWatcher(db)
	require.NoError(t, err)
	defer watcher.Close()
	settleWatcherInitialLoad(t, watcher, db)

	watcher.Start()

	// Simulate an external change (another instance touching the metadata)
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, db.SaveInstance(&statedb.InstanceRow{ID: "changed", Title: "changed"}))

	// Should receive reload signal within the poll interval
	select {
	case <-watcher.ReloadChannel():
		requireWatcherAppliedRow(t, watcher, db, "changed", "changed")
	case <-time.After(5 * time.Second):
		t.Fatal("Expected reload signal but got timeout")
	}
}

func TestStorageWatcher_NotifySaveDoesNotSuppressOwnChanges(t *testing.T) {
	db := newTestDB(t)
	watcher, err := NewStorageWatcher(db)
	require.NoError(t, err)
	defer watcher.Close()
	settleWatcherInitialLoad(t, watcher, db)
	watcher.NotifySave()
	require.NoError(t, db.SaveInstance(&statedb.InstanceRow{ID: "own", Title: "own"}))
	watcher.checkAndNotify()
	select {
	case <-watcher.ReloadChannel():
		requireWatcherAppliedRow(t, watcher, db, "own", "own")
	default:
		t.Fatal("own material change must conservatively reload")
	}
}

func TestStorageWatcher_ExternalChangesStillDetected(t *testing.T) {
	db := newTestDB(t)
	watcher, err := NewStorageWatcher(db)
	require.NoError(t, err)
	defer watcher.Close()
	settleWatcherInitialLoad(t, watcher, db)

	watcher.Start()

	// Notify that we saved
	watcher.NotifySave()

	// Wait for ignore window to expire (ignoreWindow is 3s)
	time.Sleep(4 * time.Second)

	// Now an external change should be detected
	require.NoError(t, db.SaveInstance(&statedb.InstanceRow{ID: "changed", Title: "changed"}))

	// Should receive reload signal (outside ignore window)
	select {
	case <-watcher.ReloadChannel():
		requireWatcherAppliedRow(t, watcher, db, "changed", "changed")
	case <-time.After(5 * time.Second):
		t.Fatal("Expected reload signal for external change but got timeout")
	}
}

// TestStorageWatcher_CrossProfileIsolation verifies that separate SQLite databases
// for different profiles are naturally isolated (each has its own metadata).
func TestStorageWatcher_CrossProfileIsolation(t *testing.T) {
	db1 := newTestDB(t)
	db2 := newTestDB(t)

	// Create watcher for db1 only
	watcher1, err := NewStorageWatcher(db1)
	require.NoError(t, err)
	defer watcher1.Close()
	settleWatcherInitialLoad(t, watcher1, db1)
	watcher1.Start()

	// Touch db2's metadata (simulating another profile saving)
	time.Sleep(100 * time.Millisecond)
	require.NoError(t, db2.Touch())

	// Watcher1 should NOT fire (it's watching a different database)
	select {
	case <-watcher1.ReloadChannel():
		t.Fatal("CRITICAL BUG: Watcher1 fired when db2 was modified!")
	case <-time.After(3 * time.Second):
		// Success: isolated correctly
	}

	// Watcher1 SHOULD fire when its own database changes
	require.NoError(t, db1.SaveInstance(&statedb.InstanceRow{ID: "changed", Title: "changed"}))

	select {
	case <-watcher1.ReloadChannel():
		requireWatcherAppliedRow(t, watcher1, db1, "changed", "changed")
	case <-time.After(5 * time.Second):
		t.Fatal("Watcher1 should have detected change to its own database")
	}
}

func TestStorageWatcher_NilDB(t *testing.T) {
	watcher, err := NewStorageWatcher(nil)
	require.NoError(t, err)
	require.Nil(t, watcher)
}

// Establish the applied startup snapshot before testing a later mutation.
func settleWatcherInitialLoad(t *testing.T, w *StorageWatcher, db *statedb.StateDB) {
	t.Helper()
	w.checkAndNotify()
	requireWatcherSignal(t, w)
	acknowledgeWatcher(t, w, db)
	w.checkAndNotify()
	requireNoWatcherSignal(t, w)
}
func requireWatcherAppliedRow(t *testing.T, w *StorageWatcher, db *statedb.StateDB, id, title string) {
	t.Helper()
	ticket, err := w.beginLoad()
	require.NoError(t, err)
	snapshot, err := db.LoadRegistrySnapshot()
	require.NoError(t, err)
	ticket, err = w.endLoad(ticket)
	require.NoError(t, err)
	found := false
	for _, row := range snapshot.Instances {
		if row.ID == id {
			require.Equal(t, title, row.Title)
			found = true
		}
	}
	require.True(t, found, "refresh did not contain committed row")
	w.acknowledge(ticket, snapshot, true)
}
