package ui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/web"
	"github.com/stretchr/testify/require"
)

// newUndoSweepTUIHome returns a TUI Home with one stored session "gone".
func newUndoSweepTUIHome(t *testing.T) (*Home, *session.Instance) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	h := NewHome()
	require.NotNil(t, h.storage)
	t.Cleanup(func() { _ = h.storage.Close() })
	h.width, h.height = 100, 30
	h.initialLoading = false
	inst := &session.Instance{ID: "gone", Title: "gone", Tool: "shell", Status: session.StatusStopped, CreatedAt: time.Now()}
	h.instances = []*session.Instance{inst}
	h.instanceByID = map[string]*session.Instance{"gone": inst}
	h.groupTree = session.NewGroupTree(h.instances)
	h.rebuildFlatItems()
	require.NoError(t, h.storage.SaveWithGroups(h.instances, h.groupTree))
	return h, inst
}

// Quitting the TUI makes every delete on its undo stack final: the quit
// sweeps them.
func TestTUIQuitSweepsUndoableDeletes(t *testing.T) {
	h, _ := newUndoSweepTUIHome(t)
	pending := seedChildInboxEvent(t, "gone")
	recordUISessionClosed(t)

	h.Update(sessionDeletedMsg{deletedID: "gone"})
	require.True(t, pending(), "still undoable before the quit")

	done := make(chan struct{})
	go func() { h.performFinalShutdown(false)(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown did not finish")
	}
	require.False(t, pending(), "a quit must sweep the deletes left on the undo stack")
	require.Empty(t, h.undoStack, "nothing is undoable after the quit")
}

// A restore that fails pops the entry for good: its delete is final and is
// swept.
func TestTUIFailedRestoreSweepsDelete(t *testing.T) {
	h, inst := newUndoSweepTUIHome(t)
	pending := seedChildInboxEvent(t, "gone")
	recordUISessionClosed(t)

	h.Update(sessionDeletedMsg{deletedID: "gone"})
	h.undoStack = h.undoStack[:0] // ctrl+z popped it; its Restart failed
	_, cmd := h.Update(sessionRestoredMsg{instance: inst, err: errors.New("restart failed")})
	require.True(t, pending(), "the sweep runs in a command, not in Update")
	require.NotNil(t, cmd, "a failed restore must return the sweep command")
	cmd()
	require.False(t, pending(), "a failed restore must sweep the session")
}

// newUndoSweepWebMutator returns a headless web mutator with two stored
// sessions and a pending inbox event for the first.
func newUndoSweepWebMutator(t *testing.T, profile string) (*WebMutator, func() bool) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	h, storage := newHeadlessHomeForTest(t, profile)
	first := seedSession(t, storage, nil, "web-final-001", "first")
	_ = seedSession(t, storage, []*session.Instance{first}, "web-final-002", "second")
	pending := seedChildInboxEvent(t, "web-final-001")
	recordUISessionClosed(t)
	return NewWebMutator(h), pending
}

// A lone web delete is swept once its undo window passes, with no later web
// action.
func TestWebUndoWindowTimerSweepsLoneDelete(t *testing.T) {
	m, pending := newUndoSweepWebMutator(t, "_test_web_final_timer")
	m.WithUndoWindow(30 * time.Millisecond)

	require.NoError(t, m.DeleteSession("web-final-001"))
	require.True(t, pending(), "still undoable inside the window")
	require.Eventually(t, func() bool { return !pending() }, 5*time.Second, 10*time.Millisecond,
		"a lone web delete must be swept when its undo window passes")
}

// Stopping the web server makes its undoable deletes final.
func TestWebServerShutdownSweepsUndoableDeletes(t *testing.T) {
	m, pending := newUndoSweepWebMutator(t, "_test_web_final_shutdown")
	srv := web.NewServer(web.Config{ListenAddr: "127.0.0.1:0"})
	srv.SetMutator(m)

	require.NoError(t, m.DeleteSession("web-final-001"))
	require.True(t, pending(), "still undoable while the server runs")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	require.False(t, pending(), "a server shutdown must sweep the undoable deletes")
}

// A web undo whose restart fails pops the entry for good: its delete is
// final and is swept.
func TestWebFailedRestoreSweepsDelete(t *testing.T) {
	m, pending := newUndoSweepWebMutator(t, "_test_web_final_restore")

	require.NoError(t, m.DeleteSession("web-final-001"))
	m.undoMu.Lock()
	// An account that is not configured makes Restart fail before any tmux work.
	m.undoStack[len(m.undoStack)-1].instance.Account = "not-configured"
	m.undoMu.Unlock()
	_, err := m.UndoDelete()
	require.Error(t, err)
	require.False(t, pending(), "a failed web restore must sweep the session")
}
