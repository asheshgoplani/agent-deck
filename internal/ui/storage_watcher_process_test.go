package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/stretchr/testify/require"
)

func TestStorageWatcherExternalProcessHelper(t *testing.T) {
	path := os.Getenv("WATCHER_PROCESS_DB")
	if path == "" {
		return
	}
	db, err := statedb.Open(path)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.DB().Exec("UPDATE instances SET title = ? WHERE id = ?", "raw-process-title", os.Getenv("WATCHER_PROCESS_ID"))
	require.NoError(t, err) // Deliberately no Touch: legacy metadata is unchanged.
}
func TestStorageWatcherRealProcessesOneRefresh(t *testing.T) {
	h, storage, inst := newWatcherEffectsHome(t)
	w, err := NewStorageWatcher(storage.GetDB())
	require.NoError(t, err)
	defer w.Close()
	h.storageWatcher = w
	w.checkAndNotify()
	requireWatcherSignal(t, w)
	acknowledgeWatcher(t, w, storage.GetDB())
	child := exec.Command(os.Args[0], "-test.run=^TestStorageWatcherExternalProcessHelper$", "-test.timeout=30s")
	child.Env = append(os.Environ(), "WATCHER_PROCESS_DB="+storage.Path(), "WATCHER_PROCESS_ID="+inst.ID)
	out, err := child.CombinedOutput()
	require.NoError(t, err, string(out))
	applyRefresh := func(want string) {
		w.checkAndNotify()
		requireWatcherSignal(t, w)
		ticket, err := w.beginLoad()
		require.NoError(t, err)
		instances, groups, raw, err := storage.LoadWithGroupsSnapshot()
		require.NoError(t, err)
		ticket, err = w.endLoad(ticket)
		require.NoError(t, err)
		state := h.preserveState()
		_, _ = h.Update(loadSessionsMsg{instances: instances, groups: groups, persistedSnapshot: raw, watcherTicket: &ticket, restoreState: &state})
		require.Equal(t, want, h.getInstanceByID(inst.ID).GetTitleThreadSafe())
	}
	applyRefresh("raw-process-title")
	binary := filepath.Join(t.TempDir(), "agent-deck")
	build := exec.Command("go", "build", "-p", "1", "-o", binary, "../../cmd/agent-deck")
	out, err = build.CombinedOutput()
	require.NoError(t, err, string(out))
	cli := exec.Command(binary, "-p", h.profile, "session", "set", inst.ID, "title", "cli-process-title")
	out, err = cli.CombinedOutput()
	require.NoError(t, err, string(out))
	applyRefresh("cli-process-title")
}
