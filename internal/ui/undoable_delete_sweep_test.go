package ui

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// seedChildInboxEvent queues one pending conductor inbox event for child and
// returns a func reporting whether it is still there.
func seedChildInboxEvent(t *testing.T, child string) func() bool {
	t.Helper()
	session.ResetInboxFingerprintCacheForTest()
	conductor := "cond-undo-" + child
	require.NoError(t, session.WriteInboxEvent(conductor, session.TransitionNotificationEvent{
		ChildSessionID:  child,
		ChildTitle:      "worker",
		FromStatus:      "running",
		ToStatus:        "waiting",
		LastOutputHash:  "turn-1",
		Timestamp:       time.Now(),
		TargetSessionID: conductor,
		TargetKind:      "conductor",
		DeliveryResult:  "deferred_target_busy",
	}))
	return func() bool {
		raw, err := os.ReadFile(session.InboxPathFor(conductor))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return strings.Contains(string(raw), child)
	}
}

// A TUI delete can be undone (ctrl+z), so it only announces the removal: the
// session's pending inbox events survive the delete and the undo, and are
// swept once the entry is pushed off the undo stack (the delete is final).
func TestTUIUndoableDeleteDefersInboxSweep(t *testing.T) {
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
	pending := seedChildInboxEvent(t, "gone")
	frames := recordUISessionClosed(t)

	h.Update(sessionDeletedMsg{deletedID: "gone"})
	require.Equal(t, []string{h.storage.Profile() + "/gone/removed"}, frames(), "the delete is announced at once")
	require.True(t, pending(), "an undoable delete must keep the session's pending inbox events")
	if _, cmd := h.Update(tea.KeyMsg{Type: tea.KeyCtrlZ}); cmd == nil {
		t.Fatal("ctrl+z did not start the undo")
	}
	require.True(t, pending(), "an undone delete keeps the session's pending inbox events")
	require.Len(t, frames(), 1, "undo publishes nothing extra")

	// Delete again, then push it off the 10-entry stack: now it is final.
	// The sweep runs in the returned command, not in Update.
	require.Nil(t, h.pushUndoStack(inst))
	var evictSweep tea.Cmd
	for i := 0; i < 10; i++ {
		require.True(t, pending(), "swept while still undoable (push %d)", i)
		evictSweep = h.pushUndoStack(&session.Instance{ID: fmt.Sprintf("other-%d", i)})
	}
	require.True(t, pending(), "the eviction sweep must not run inside Update")
	require.NotNil(t, evictSweep, "evicting an entry must return its sweep command")
	evictSweep()
	require.False(t, pending(), "a delete pushed off the undo stack must be swept")
}

// A web delete can be undone within the undo window: the sweep waits until
// the entry expires or is evicted.
func TestWebUndoableDeleteDefersInboxSweep(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	h, storage := newHeadlessHomeForTest(t, "_test_web_undo_sweep")
	first := seedSession(t, storage, nil, "web-undo-001", "first")
	_ = seedSession(t, storage, []*session.Instance{first}, "web-undo-002", "second")
	pending := seedChildInboxEvent(t, "web-undo-001")
	frames := recordUISessionClosed(t)
	m := NewWebMutator(h)

	require.NoError(t, m.DeleteSession("web-undo-001"))
	require.Len(t, frames(), 1, "the delete is announced at once")
	require.True(t, pending(), "an undoable web delete must keep the session's pending inbox events")

	m.undoWindow = time.Millisecond
	time.Sleep(5 * time.Millisecond)
	require.NoError(t, m.DeleteSession("web-undo-002"))
	require.False(t, pending(), "a web delete past its undo window must be swept")
}
