package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/stretchr/testify/require"
)

// A web undo re-saves the very instance its delete removed. That instance
// still remembered the deleted row, so the registry refused the save as a
// stale concurrent deletion and the undone session was never written back.
func TestWebUndoReSavesTheDeletedInstance(t *testing.T) {
	m, _ := newUndoSweepWebMutator(t, "_test_web_undo_resave")
	prev := restartForUndo
	restartForUndo = func(*session.Instance) error { return nil }
	t.Cleanup(func() { restartForUndo = prev })

	require.NoError(t, m.DeleteSession("web-final-001"))
	id, err := m.UndoDelete()
	require.NoError(t, err, "undo must re-save the deleted instance")
	require.Equal(t, "web-final-001", id)

	rows, _, err := m.h.storage.LoadLite()
	require.NoError(t, err)
	found := false
	for _, r := range rows {
		if r.ID == "web-final-001" {
			found = true
		}
	}
	require.True(t, found, "the undone session must be back in storage")
}

// The TUI pushes a deleted instance onto its undo stack the same way: it must
// no longer carry the deleted row's snapshot.
func TestTUIUndoStackForgetsDeletedRowSnapshot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	h, storage := newHeadlessHomeForTest(t, "_test_tui_undo_resave")
	inst := seedSession(t, storage, nil, "tui-undo-001", "first") // remembers its stored row

	require.NoError(t, storage.DeleteInstance("tui-undo-001"))
	h.pushUndoStack(inst)

	all := []*session.Instance{inst}
	require.NoError(t, storage.SaveWithGroups(all, session.NewGroupTree(all)),
		"an instance on the TUI undo stack must re-save as a new row")
}
