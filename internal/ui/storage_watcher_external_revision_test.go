package ui

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/stretchr/testify/require"
)

// This regression deliberately does not start the polling goroutine. Each
// check is one refresh, and the second handle performs a real durable write.
func TestStorageWatcherExternalWriteInsideOwnSaveWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	reader, err := statedb.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close() })
	require.NoError(t, reader.Migrate())
	writer, err := statedb.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = writer.Close() })
	watcher, err := NewStorageWatcher(reader)
	require.NoError(t, err)
	t.Cleanup(func() { _ = watcher.Close() })
	settleWatcherInitialLoad(t, watcher, reader)

	watcher.NotifySave()
	require.NoError(t, writer.SaveInstance(&statedb.InstanceRow{
		ID: "external", Title: "external committed title", Tool: "claude",
		Status: "stopped", ProjectPath: t.TempDir(), CreatedAt: time.Now(),
	}))
	require.NoError(t, writer.Touch())
	committed, err := reader.LoadInstanceByID("external")
	require.NoError(t, err)
	require.NotNil(t, committed)
	require.Equal(t, "external committed title", committed.Title)

	watcher.checkAndNotify()
	select {
	case <-watcher.ReloadChannel():
		requireWatcherAppliedRow(t, watcher, reader, "external", "external committed title")
	default:
		t.Error("first refresh lost a committed external write inside the own-save window")
	}

	// No further writes are needed for the required first-refresh notification.
}
