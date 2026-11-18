package ui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Undo after a delete restarted every session, so an archived or stopped
// session came back running (TUI-7). Undo now restores the state the session
// had when it was deleted: an active session is restarted, a stopped, errored
// or archived one comes back as it was, without a process.

// countUndoRestarts stubs the undo restart seam and counts its calls.
func countUndoRestarts(t *testing.T) *int {
	t.Helper()
	n := 0
	prev := restartForUndo
	restartForUndo = func(*session.Instance) error { n++; return nil }
	t.Cleanup(func() { restartForUndo = prev })
	return &n
}

// tuiDeleteThenUndo deletes inst through the TUI's delete command and undoes
// it with ctrl+z, returning the restore message.
func tuiDeleteThenUndo(t *testing.T, h *Home, inst *session.Instance) sessionRestoredMsg {
	t.Helper()
	deleted := h.deleteSession(inst)()
	_, cleanup := h.Update(deleted)
	runAllCmds(cleanup) // the undo waits for the delete's hook cleanup
	_, cmd := h.Update(tea.KeyMsg{Type: tea.KeyCtrlZ})
	require.NotNil(t, cmd, "ctrl+z after a delete must return the restore command")
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		restored, ok := msg.(sessionRestoredMsg)
		require.True(t, ok, "undo returned %T", msg)
		return restored
	case <-time.After(30 * time.Second):
		t.Fatal("undo did not finish")
	}
	return sessionRestoredMsg{}
}

func TestTUIUndoDelete_RestoresPreviousState(t *testing.T) {
	cases := []struct {
		name        string
		status      session.Status
		archived    bool
		wantRestart bool
	}{
		{"stopped stays stopped", session.StatusStopped, false, false},
		{"errored stays errored", session.StatusError, false, false},
		{"archived stays archived and stopped", session.StatusStopped, true, false},
		{"active session is restarted", session.StatusIdle, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restarts := countUndoRestarts(t)
			h, inst := newUndoSweepTUIHome(t)
			t.Cleanup(func() { _ = inst.Kill() }) // unfixed code starts a real process
			inst.Status = tc.status
			if tc.archived {
				inst.SetArchivedAtThreadSafe(time.Now().UTC())
			}

			restored := tuiDeleteThenUndo(t, h, inst)
			require.NoError(t, restored.err)
			if tc.wantRestart {
				require.Equal(t, 1, *restarts, "an active session is restarted by undo")
				return
			}
			require.Zero(t, *restarts, "undo restarted a session that was %s (archived=%v)", tc.status, tc.archived)
			require.False(t, inst.Exists(), "undo left a running process for a session that had none")
			require.Equal(t, tc.status, inst.GetStatusThreadSafe(), "undo must restore the status the session had")
			require.Equal(t, tc.archived, inst.IsArchived(), "undo must keep the archived flag")
		})
	}
}

func TestWebUndoDelete_RestoresPreviousState(t *testing.T) {
	cases := []struct {
		name        string
		status      session.Status
		archived    bool
		wantRestart bool
	}{
		{"stopped stays stopped", session.StatusStopped, false, false},
		{"archived stays archived", session.StatusStopped, true, false},
		{"active session is restarted", session.StatusWaiting, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restarts := countUndoRestarts(t)
			m, _ := newUndoSweepWebMutator(t, "_test_web_undo_state")
			m.h.instancesMu.RLock()
			inst := m.h.instanceByID["web-final-001"]
			m.h.instancesMu.RUnlock()
			if inst == nil {
				// Headless: the mutator loads the row on demand; seed the
				// state through storage instead.
				rows, _, err := m.h.storage.LoadWithGroups()
				require.NoError(t, err)
				for _, r := range rows {
					if r.ID == "web-final-001" {
						r.Status = tc.status
						if tc.archived {
							r.SetArchivedAtThreadSafe(time.Now().UTC())
						}
					}
				}
				require.NoError(t, m.h.storage.SaveWithGroups(rows, session.NewGroupTree(rows)))
			} else {
				inst.Status = tc.status
				if tc.archived {
					inst.SetArchivedAtThreadSafe(time.Now().UTC())
				}
			}

			require.NoError(t, m.DeleteSession("web-final-001"))
			_, err := m.UndoDelete()
			require.NoError(t, err)
			if tc.wantRestart {
				require.Equal(t, 1, *restarts, "an active session is restarted by undo")
				return
			}
			require.Zero(t, *restarts, "web undo restarted a session that was %s (archived=%v)", tc.status, tc.archived)

			rows, _, err := m.h.storage.LoadWithGroups()
			require.NoError(t, err)
			var back *session.Instance
			for _, r := range rows {
				if r.ID == "web-final-001" {
					back = r
				}
			}
			require.NotNil(t, back, "undo must bring the row back")
			require.Equal(t, tc.status, back.Status, "web undo must restore the status the session had")
			require.Equal(t, tc.archived, back.IsArchived(), "web undo must keep the archived flag")
		})
	}
}
