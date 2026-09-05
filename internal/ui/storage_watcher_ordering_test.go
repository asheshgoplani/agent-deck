package ui

import (
	"errors"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/stretchr/testify/require"
)

func TestStorageWatcherHomeOrdersLoadsWithoutWatcher(t *testing.T) {
	h, storage, inst := newWatcherEffectsHome(t)
	older := h.sessionLoadCmd(nil, false)
	oldResult := older()
	require.NoError(t, storage.GetDB().SaveInstance(&statedb.InstanceRow{ID: inst.ID, Title: "newer", Tool: "shell", Status: "stopped", ProjectPath: inst.ProjectPath}))
	newer := h.sessionLoadCmd(nil, false)
	_, _ = h.Update(newer())
	_, _ = h.Update(oldResult)
	require.Equal(t, "newer", h.getInstanceByID(inst.ID).GetTitleThreadSafe())
}

func TestStorageWatcherHomeIssuancePrecedesCommandExecution(t *testing.T) {
	h, storage, inst := newWatcherEffectsHome(t)
	w, err := NewStorageWatcher(storage.GetDB())
	require.NoError(t, err)
	defer w.Close()
	h.storageWatcher = w
	older := h.sessionLoadCmd(nil, false)
	newer := h.sessionLoadCmd(nil, false)
	newResult := newer().(loadSessionsMsg)
	// Even if an older command executes later, it must not acquire a newer ticket.
	oldResult := older().(loadSessionsMsg)
	require.True(t, w.current(*newResult.watcherTicket))
	require.False(t, w.current(*oldResult.watcherTicket))
	_, _ = h.Update(newResult)
	_, _ = h.Update(oldResult)
	require.Equal(t, inst.ID, h.instances[0].ID)
}

func TestStorageWatcherHomeRejectsReplacedWatcher(t *testing.T) {
	h, storage, _ := newWatcherEffectsHome(t)
	first, err := NewStorageWatcher(storage.GetDB())
	require.NoError(t, err)
	defer first.Close()
	h.storageWatcher = first
	cmd := h.sessionLoadCmd(nil, false)
	msg := cmd().(loadSessionsMsg)
	second, err := NewStorageWatcher(storage.GetDB())
	require.NoError(t, err)
	defer second.Close()
	h.storageWatcher = second
	h.instances = nil
	_, _ = h.Update(msg)
	require.Empty(t, h.instances, "message from replaced watcher applied")
}

func TestStorageWatcherImportCompletionUsesOrderedRead(t *testing.T) {
	h, storage, inst := newWatcherEffectsHome(t)
	older := h.sessionLoadCmd(nil, false)
	oldResult := older()
	require.NoError(t, storage.GetDB().SaveInstance(&statedb.InstanceRow{ID: inst.ID, Title: "after import", Tool: "shell", Status: "stopped", ProjectPath: inst.ProjectPath}))
	_, cmd := h.Update(importReloadMsg{})
	require.NotNil(t, cmd)
	_, _ = h.Update(cmd())
	_, _ = h.Update(oldResult)
	require.Equal(t, "after import", h.getInstanceByID(inst.ID).GetTitleThreadSafe())
}

func TestStorageWatcherImportFailureDoesNotAcknowledge(t *testing.T) {
	h, storage, _ := newWatcherEffectsHome(t)
	w, err := NewStorageWatcher(storage.GetDB())
	require.NoError(t, err)
	defer w.Close()
	h.storageWatcher = w
	acknowledged := w.acknowledged
	_, _ = h.Update(loadSessionsMsg{err: errors.New("discovery failed")})
	require.Same(t, acknowledged, w.acknowledged)
	require.True(t, w.pending)
	require.NoError(t, storage.Close())
	_, cmd := h.Update(importReloadMsg{})
	_, _ = h.Update(cmd())
	require.Same(t, acknowledged, w.acknowledged)
	require.True(t, w.pending)
	require.Error(t, h.err)
}
