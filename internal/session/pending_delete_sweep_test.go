package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// setupPendingSweepStore returns a storage with the given live sessions, a
// pending conductor inbox event and a notify-state record for each id in
// seeded, and reports which of them still have inbox lines / records.
func setupPendingSweepStore(t *testing.T, live []string, seeded []string) (s *Storage, inboxHas, recordHas func(id string) bool) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	ResetInboxFingerprintCacheForTest()

	s, err := NewStorageWithProfile("_test_pending_sweep")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	var insts []*Instance
	for _, id := range live {
		insts = append(insts, &Instance{ID: id, Title: id, Tool: "shell", ProjectPath: "/tmp", Status: StatusStopped, CreatedAt: time.Now()})
	}
	require.NoError(t, s.SaveWithGroups(insts, NewGroupTree(insts)))

	const conductor = "cond-pending-sweep"
	records := map[string]transitionNotifyRecord{}
	for i, id := range seeded {
		require.NoError(t, WriteInboxEvent(conductor, TransitionNotificationEvent{
			ChildSessionID:  id,
			ChildTitle:      id,
			FromStatus:      "running",
			ToStatus:        "waiting",
			LastOutputHash:  "turn-" + strconv.Itoa(i),
			Timestamp:       time.Now(),
			TargetSessionID: conductor,
			TargetKind:      "conductor",
			DeliveryResult:  "deferred_target_busy",
		}))
		records[id] = transitionNotifyRecord{From: "running", To: "waiting", At: time.Now().Unix()}
	}
	statePath := transitionNotifyStatePath()
	require.NoError(t, os.MkdirAll(filepath.Dir(statePath), 0o755))
	raw, err := json.Marshal(transitionNotifyState{Records: records})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(statePath, raw, 0o644))

	inboxHas = func(id string) bool {
		data, err := os.ReadFile(InboxPathFor(conductor))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return strings.Contains(string(data), `"child_session_id":"`+id+`"`)
	}
	recordHas = func(id string) bool {
		data, err := os.ReadFile(statePath)
		require.NoError(t, err)
		var st transitionNotifyState
		require.NoError(t, json.Unmarshal(data, &st))
		_, ok := st.Records[id]
		return ok
	}
	return s, inboxHas, recordHas
}

// A process that crashed with undoable deletes leaves their pending records.
// The startup reconcile sweeps those orphans, and keeps the records of live
// sessions, of deletes another running process can still undo, and of ids
// nobody recorded as deleted.
func TestReconcileRemovedSessionSweepsOrphansKeepsLive(t *testing.T) {
	s, inboxHas, recordHas := setupPendingSweepStore(t,
		[]string{"live-restored", "live-other"},
		[]string{"orphan", "live-restored", "live-other", "undoable-elsewhere", "never-marked"})

	db := s.GetDB()
	const deadOwner = "2147483646:1" // no such pid
	require.NoError(t, db.SetMeta(pendingDeleteSweepPrefix+"orphan", deadOwner))
	require.NoError(t, db.SetMeta(pendingDeleteSweepPrefix+"live-restored", deadOwner))
	// The parent process is running: its delete may still be undone.
	require.NoError(t, db.SetMeta(pendingDeleteSweepPrefix+"undoable-elsewhere", strconv.Itoa(os.Getppid())+":1"))
	// This process: its own undo stack still holds the delete.
	require.NoError(t, s.MarkPendingDeleteSweep("mine"))

	swept, err := s.ReconcileRemovedSessionSweeps()
	require.NoError(t, err)
	require.Contains(t, swept, "orphan")
	require.Len(t, swept, 1, "only the orphan is swept")

	require.False(t, inboxHas("orphan"), "the orphan's inbox lines must be swept")
	require.False(t, recordHas("orphan"), "the orphan's notify-state record must be swept")
	for _, id := range []string{"live-restored", "live-other", "undoable-elsewhere", "never-marked"} {
		require.True(t, inboxHas(id), "%s's inbox lines must be kept", id)
		require.True(t, recordHas(id), "%s's notify-state record must be kept", id)
	}

	left, err := db.ListMetaPrefix(pendingDeleteSweepPrefix)
	require.NoError(t, err)
	require.NotContains(t, left, pendingDeleteSweepPrefix+"orphan", "a swept record is cleared")
	require.NotContains(t, left, pendingDeleteSweepPrefix+"live-restored", "a restored session's record is cleared")
	require.Contains(t, left, pendingDeleteSweepPrefix+"undoable-elsewhere")
	require.Contains(t, left, pendingDeleteSweepPrefix+"mine")
}

// The same pid with another start time is an earlier image of this process
// (an exec restart): its records are orphans.
func TestReconcileRemovedSessionSweepsEarlierImageOfThisPid(t *testing.T) {
	s, inboxHas, _ := setupPendingSweepStore(t, nil, []string{"before-exec"})
	require.NoError(t, s.GetDB().SetMeta(pendingDeleteSweepPrefix+"before-exec", strconv.Itoa(os.Getpid())+":1"))
	swept, err := s.ReconcileRemovedSessionSweeps()
	require.NoError(t, err)
	require.Contains(t, swept, "before-exec")
	require.False(t, inboxHas("before-exec"))
}

// When storage cannot be read the reconcile sweeps nothing.
func TestReconcileRemovedSessionSweepsSkipsUnreadableStorage(t *testing.T) {
	s, inboxHas, recordHas := setupPendingSweepStore(t, nil, []string{"orphan"})
	require.NoError(t, s.GetDB().SetMeta(pendingDeleteSweepPrefix+"orphan", "2147483646:1"))
	require.NoError(t, s.Close())
	_, err := s.ReconcileRemovedSessionSweeps()
	require.Error(t, err)
	require.True(t, inboxHas("orphan"))
	require.True(t, recordHas("orphan"))
}
