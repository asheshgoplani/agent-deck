//go:build watcher_proof

package ui

import (
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestStorageWatcherProofSynchronousIssuanceDoesNotProbe(t *testing.T) {
	h, storage, _ := newWatcherEffectsHome(t)
	w, err := NewStorageWatcher(storage.GetDB())
	require.NoError(t, err)
	defer w.Close()
	h.storageWatcher = w
	release := statedb.HoldRegistryObserverForProof(w.observer)
	done := make(chan struct{})
	go func() { _, _ = h.Update(storageChangedMsg{}); close(done) }()
	// A held mutex is the deterministic cause, not a slow database or sleep.
	select {
	case <-done:
		release()
	case <-time.After(time.Second):
		release()
		<-done
		t.Fatal("Update waited for observer I/O lock before returning its command")
	}
}

func TestStorageWatcherProofDelayedUnticketedLoad(t *testing.T) {
	for _, kind := range []string{"initial", "manual"} {
		t.Run(kind, func(t *testing.T) {
			h, storage, inst := newWatcherEffectsHome(t)
			inst.Tool = "shell"
			require.NoError(t, storage.SaveWithGroups(h.instances, h.groupTree))
			w, err := NewStorageWatcher(storage.GetDB())
			require.NoError(t, err)
			defer w.Close()
			h.storageWatcher = w
			var old tea.Msg
			if kind == "initial" {
				old = h.loadSessions()
			} else {
				_, cmd := h.handleMainKey(tea.KeyMsg{Type: tea.KeyCtrlR})
				require.NotNil(t, cmd)
				old = cmd()
			}
			// Deliver the old asynchronous result only after a newer watcher result.
			require.NoError(t, storage.GetDB().SaveInstance(&statedb.InstanceRow{ID: inst.ID, Title: "newest", Tool: "shell", Status: "stopped", ProjectPath: inst.ProjectPath}))
			_, batch := h.Update(storageChangedMsg{})
			require.NotNil(t, batch)
			commands := batch().(tea.BatchMsg)
			require.NotEmpty(t, commands)
			_, _ = h.Update(commands[0]())
			require.Equal(t, "newest", h.getInstanceByID(inst.ID).GetTitleThreadSafe())
			_, _ = h.Update(old)
			require.Equal(t, "newest", h.getInstanceByID(inst.ID).GetTitleThreadSafe(), "old persisted load replaced newer applied state")
		})
	}
}
