package ui

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/stretchr/testify/require"
)

func newWatcherEffectsHome(t *testing.T) (*Home, *session.Storage, *session.Instance) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	storage, err := session.NewStorageWithProfile("_watcher_effects")
	require.NoError(t, err)
	t.Cleanup(func() { _ = storage.Close() })
	h := NewHome()
	if h.storageWatcher != nil {
		_ = h.storageWatcher.Close()
	}
	h.storageWatcher = nil
	h.storage = storage
	h.profile = "_watcher_effects"
	h.width, h.height = 100, 30
	inst := session.NewInstanceWithTool("original", t.TempDir(), "opencode")
	inst.OpenCodeDetectedAt = time.Now().Add(-time.Minute)
	h.instances = []*session.Instance{inst}
	h.instanceByID = map[string]*session.Instance{inst.ID: inst}
	h.groupTree = session.NewGroupTree(h.instances)
	require.NoError(t, storage.SaveWithGroups(h.instances, h.groupTree))
	return h, storage, inst
}

// Repeated unsuccessful detection is the save-producing edge of the reload
// feedback cycle. A completed identical negative result must be idempotent.
func TestStorageWatcherRepeatedNegativeDetectionDoesNotSave(t *testing.T) {
	h, storage, inst := newWatcherEffectsHome(t)
	before, err := storage.GetDB().LastModified()
	require.NoError(t, err)
	completedAt := inst.OpenCodeDetectedAt
	_, _ = h.Update(openCodeDetectionCompleteMsg{instanceID: inst.ID})
	after, err := storage.GetDB().LastModified()
	require.NoError(t, err)
	require.Equal(t, before, after, "identical negative detection must not create another watcher revision")
	require.Equal(t, completedAt, inst.OpenCodeDetectedAt, "completed negative result must not refresh its timestamp on every reload")
}

// A real closed database makes the reapplied save fail deterministically.
// The pending intent must survive so a successful later retry can persist it.
func TestStorageWatcherReloadFailedSaveRetainsPendingTitle(t *testing.T) {
	h, storage, inst := newWatcherEffectsHome(t)
	inst.Tool = "shell" // this control is independent of OpenCode detection
	h.pendingTitleChanges = map[string]pendingTitle{
		inst.ID: {title: "operator pending rename", locked: true},
	}
	state := h.preserveState()
	require.NoError(t, storage.Close())
	_, _ = h.Update(loadSessionsMsg{instances: []*session.Instance{inst}, restoreState: &state})
	require.Error(t, h.err, "control must reach the failing storage save")
	require.Equal(t, "operator pending rename", h.getInstanceByID(inst.ID).GetTitleThreadSafe())
	pending, exists := h.pendingTitleChanges[inst.ID]
	require.True(t, exists, "failed save discarded the only durable-retry intent")
	require.Equal(t, "operator pending rename", pending.title)
}

func TestStorageWatcherNegativeThenExplicitPositive(t *testing.T) {
	h, storage, inst := newWatcherEffectsHome(t)
	_, _ = h.Update(openCodeDetectionCompleteMsg{instanceID: inst.ID, sessionID: "explicit-later-session"})
	row, err := storage.GetDB().LoadInstanceByID(inst.ID)
	require.NoError(t, err)
	require.Contains(t, string(row.ToolData), "explicit-later-session")
}

func TestStorageWatcherReloadFailedSaveRetainsPendingGroup(t *testing.T) {
	h, storage, inst := newWatcherEffectsHome(t)
	inst.Tool = "shell"
	h.pendingGroupOps = []pendingGroupOp{{kind: groupOpCreate, name: "pending-group"}}
	state := h.preserveState()
	require.NoError(t, storage.Close())
	_, _ = h.Update(loadSessionsMsg{instances: []*session.Instance{inst}, restoreState: &state})
	require.Error(t, h.err)
	require.NotNil(t, h.groupTree.Groups["pending-group"])
	require.Len(t, h.pendingGroupOps, 1, "failed save must retain group retry intent")
}
