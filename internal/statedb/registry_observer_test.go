package statedb

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestRegistryObserverLiveCommitAndReadOnly(t *testing.T) {
	db := newTestDB(t)
	observer, err := db.NewRegistryObserver()
	require.NoError(t, err)
	defer observer.Close()
	before, err := observer.Version()
	require.NoError(t, err)
	require.NoError(t, db.SaveInstance(&InstanceRow{ID: "external", Title: "external", Status: "running"}))
	snapshot, _, after, err := observer.Snapshot()
	require.NoError(t, err)
	require.NotEqual(t, before, after)
	require.Len(t, snapshot.Instances, 1)
	require.Equal(t, "running", snapshot.Instances[0].Status)
	_, err = observer.conn.ExecContext(context.Background(), "UPDATE instances SET title='forbidden'")
	require.Error(t, err)
}
func TestRegistryObserverCloseCancelsAndStopsReads(t *testing.T) {
	db := newTestDB(t)
	observer, err := db.NewRegistryObserver()
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- observer.Close() }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Close blocked")
	}
	_, err = observer.Version()
	require.Error(t, err)
	require.NoError(t, observer.Close())
}

func TestRegistryObserverReconnectChangesEpoch(t *testing.T) {
	db := newTestDB(t)
	o, err := db.NewRegistryObserver()
	require.NoError(t, err)
	defer o.Close()
	_, old, err := o.Probe()
	require.NoError(t, err)
	require.NoError(t, o.conn.Close())
	_, _, err = o.Probe()
	require.Error(t, err, "failed read must not become a success during recovery")
	_, next, err := o.Probe()
	require.NoError(t, err)
	require.Greater(t, next, old)
	require.NoError(t, db.SaveInstance(&InstanceRow{ID: "recovered", Title: "recovered"}))
	snap, _, _, err := o.Snapshot()
	require.NoError(t, err)
	require.Len(t, snap.Instances, 1)
}
