package ui

import (
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/stretchr/testify/require"
	"testing"
)

func requireWatcherSignal(t *testing.T, w *StorageWatcher) {
	t.Helper()
	select {
	case <-w.ReloadChannel():
	default:
		t.Fatal("expected reload")
	}
}
func requireNoWatcherSignal(t *testing.T, w *StorageWatcher) {
	t.Helper()
	select {
	case <-w.ReloadChannel():
		t.Fatal("unexpected reload")
	default:
	}
}
func acknowledgeWatcher(t *testing.T, w *StorageWatcher, db *statedb.StateDB) {
	t.Helper()
	ticket, err := w.beginLoad()
	require.NoError(t, err)
	snapshot, err := db.LoadRegistrySnapshot()
	require.NoError(t, err)
	ticket, err = w.endLoad(ticket)
	require.NoError(t, err)
	w.acknowledge(ticket, snapshot, true)
}
func TestStorageWatcherMaterialChangesIgnoreTimestamp(t *testing.T) {
	for _, stamp := range []string{"0", "100", "-1"} {
		t.Run(stamp, func(t *testing.T) {
			db := newTestDB(t)
			require.NoError(t, db.SetMeta("last_modified", stamp))
			w, err := NewStorageWatcher(db)
			require.NoError(t, err)
			defer w.Close()
			settleWatcherInitialLoad(t, w, db)
			require.NoError(t, db.SaveInstance(&statedb.InstanceRow{ID: "a", Title: "one", Status: "stopped"}))
			require.NoError(t, db.SetMeta("last_modified", stamp))
			w.checkAndNotify()
			requireWatcherSignal(t, w)
			requireWatcherAppliedRow(t, w, db, "a", "one")
			w.checkAndNotify()
			requireNoWatcherSignal(t, w)
			require.NoError(t, db.SaveInstance(&statedb.InstanceRow{ID: "a", Title: "one", Status: "running"}))
			w.checkAndNotify()
			requireWatcherSignal(t, w)
			row, err := db.LoadInstanceByID("a")
			require.NoError(t, err)
			require.Equal(t, "running", row.Status)
			requireWatcherAppliedRow(t, w, db, "a", "one")
		})
	}
}
func TestStorageWatcherMetadataOnlyDoesNotReload(t *testing.T) {
	db := newTestDB(t)
	w, err := NewStorageWatcher(db)
	require.NoError(t, err)
	defer w.Close()
	w.checkAndNotify()
	requireWatcherSignal(t, w)
	acknowledgeWatcher(t, w, db)
	require.NoError(t, db.Touch())
	w.checkAndNotify()
	requireNoWatcherSignal(t, w)
}
func TestStorageWatcherFailedAndStaleLoadCannotAcknowledge(t *testing.T) {
	db := newTestDB(t)
	w, err := NewStorageWatcher(db)
	require.NoError(t, err)
	defer w.Close()
	old, err := w.beginLoad()
	require.NoError(t, err)
	latest, err := w.beginLoad()
	require.NoError(t, err)
	require.False(t, w.current(old))
	require.True(t, w.current(latest))
	w.acknowledge(latest, nil, false)
	w.checkAndNotify()
	requireWatcherSignal(t, w)
	w.acknowledge(old, &statedb.RegistrySnapshotResult{}, true)
	w.checkAndNotify()
	requireWatcherSignal(t, w)
	acknowledgeWatcher(t, w, db)
	w.checkAndNotify()
	requireNoWatcherSignal(t, w)
}
func TestStorageWatcherCommitDuringLoadSurvivesAck(t *testing.T) {
	db := newTestDB(t)
	w, err := NewStorageWatcher(db)
	require.NoError(t, err)
	defer w.Close()
	ticket, err := w.beginLoad()
	require.NoError(t, err)
	snapshot, err := db.LoadRegistrySnapshot()
	require.NoError(t, err)
	require.NoError(t, db.SaveInstance(&statedb.InstanceRow{ID: "late", Title: "late"}))
	ticket, err = w.endLoad(ticket)
	require.NoError(t, err)
	w.acknowledge(ticket, snapshot, true)
	w.checkAndNotify()
	requireWatcherSignal(t, w)
	acknowledgeWatcher(t, w, db)
	w.checkAndNotify()
	requireNoWatcherSignal(t, w)
}
func TestStorageWatcherCloseIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	w, err := NewStorageWatcher(db)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.NoError(t, w.Close())
	w.TriggerReload()
	w.checkAndNotify()
	requireNoWatcherSignal(t, w)
}

func TestStorageWatcherInitialUnboundLoadRequiresRefresh(t *testing.T) {
	db := newTestDB(t)
	w, err := NewStorageWatcher(db)
	require.NoError(t, err)
	defer w.Close()
	w.checkAndNotify()
	requireWatcherSignal(t, w)
	acknowledgeWatcher(t, w, db)
	w.checkAndNotify()
	requireNoWatcherSignal(t, w)
}
func TestStorageWatcherEpochChangeCannotAcknowledge(t *testing.T) {
	db := newTestDB(t)
	w, err := NewStorageWatcher(db)
	require.NoError(t, err)
	defer w.Close()
	ticket, err := w.beginLoad()
	require.NoError(t, err)
	snap, err := db.LoadRegistrySnapshot()
	require.NoError(t, err)
	ticket, err = w.endLoad(ticket)
	require.NoError(t, err)
	ticket.afterEpoch++
	w.acknowledge(ticket, snap, true)
	requireWatcherSignal(t, w)
}
