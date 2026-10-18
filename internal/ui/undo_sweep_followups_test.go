package ui

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// pretendOwnerExited rewrites every pending delete-sweep record as owned by
// a process that is gone, as the next start would find it.
func pretendOwnerExited(t *testing.T, storage *session.Storage) {
	t.Helper()
	db := storage.GetDB()
	recs, err := db.ListMetaPrefix("pending_delete_sweep:")
	require.NoError(t, err)
	for k := range recs {
		require.NoError(t, db.SetMeta(k, "2147483646:1"))
	}
}

// A successful TUI undo clears the delete's pending record: a later
// reconcile, after the session moved to another profile (its row is gone
// from this one), must not sweep the live session's inbox entries.
func TestTUISuccessfulUndoThenMoveReconcileSweepsNothing(t *testing.T) {
	h, inst := newUndoSweepTUIHome(t)
	pending := seedChildInboxEvent(t, "gone")
	recordUISessionClosed(t)

	h.Update(sessionDeletedMsg{deletedID: "gone"})
	h.undoStack = h.undoStack[:0] // ctrl+z popped it; its Restart succeeded
	_, cmd := h.Update(sessionRestoredMsg{instance: inst})
	runAllCmds(cmd)

	require.NoError(t, h.storage.DeleteInstance("gone")) // moved to another profile
	pretendOwnerExited(t, h.storage)
	swept, err := h.storage.ReconcileRemovedSessionSweeps()
	require.NoError(t, err)
	require.Empty(t, swept, "a reconcile after a successful undo must sweep nothing")
	require.True(t, pending(), "the moved session's inbox entries must be kept")
}

// The same for a web undo.
func TestWebSuccessfulUndoThenMoveReconcileSweepsNothing(t *testing.T) {
	m, pending := newUndoSweepWebMutator(t, "_test_web_undo_move")
	prev := restartForUndo
	restartForUndo = func(*session.Instance) error { return nil }
	t.Cleanup(func() { restartForUndo = prev })

	require.NoError(t, m.DeleteSession("web-final-001"))
	id, err := m.UndoDelete()
	require.NoError(t, err)
	require.Equal(t, "web-final-001", id)

	storage := m.h.storage
	require.NoError(t, storage.DeleteInstance("web-final-001")) // moved to another profile
	pretendOwnerExited(t, storage)
	swept, err := storage.ReconcileRemovedSessionSweeps()
	require.NoError(t, err)
	require.Empty(t, swept, "a reconcile after a successful web undo must sweep nothing")
	require.True(t, pending(), "the moved session's inbox entries must be kept")
}

// On a signal the web undo stack's sweep waits at most its cap.
func TestWebFinalizePendingDeletesWithinIsBounded(t *testing.T) {
	m, _ := newUndoSweepWebMutator(t, "_test_web_final_bounded")
	release := make(chan struct{})
	returned := make(chan struct{})
	prev := finalizeRemovedSessionsFn
	finalizeRemovedSessionsFn = func(*session.Storage, string, []string) {
		defer close(returned)
		<-release
	}
	// The bounded sweep keeps running after its cap: unblock it and wait for
	// it to return before restoring the seam, or the race detector sees the
	// restore race its read of finalizeRemovedSessionsFn.
	t.Cleanup(func() {
		close(release)
		<-returned
		finalizeRemovedSessionsFn = prev
	})

	require.NoError(t, m.DeleteSession("web-final-001"))
	done := make(chan struct{})
	go func() { m.FinalizePendingDeletesWithin(50 * time.Millisecond); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the signal-path web sweep waited past 2s; want at most its cap")
	}
}

// runAllCmds runs cmd and every command a tea.BatchMsg it returns holds,
// giving each at most two seconds.
func runAllCmds(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				runAllCmds(c)
			}
		}
	case <-time.After(2 * time.Second):
	}
}
