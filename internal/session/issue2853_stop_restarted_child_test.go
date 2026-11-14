package session

// Issue #2853: a child that was stopped and started again read error for a
// pass, and its parent got an urgent running -> error notice.
//
// Claude emits SessionEnd when a stop kills it; the hook file then holds
// status "dead" for the old process. It stays inside the 2 minute hook fast
// path window, so when the same child is started again within that window
// the notify daemon found a live pane plus a fresh "dead" hook and committed
// error for the new process. A child's first run has no hook file yet, which
// is why a single stop of a fresh child never showed it.
//
// The test follows the report: a parent and a child, the daemon polling, and
// four cycles of start, UserPromptSubmit, stop, SessionEnd. The start is
// modeled the way Start() does it: the tmux session is created under the
// spawn lock and the spawn is stamped after, and the start's own save of the
// row comes last. The daemon polls inside each of those windows.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// writeIssue2853Hook writes the child's hook status file the way the hook
// handler does (status, event, unix-seconds ts).
func writeIssue2853Hook(t *testing.T, instanceID, status, event string, at time.Time) {
	t.Helper()
	dir := GetHooksDir()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	body := fmt.Sprintf(`{"status":%q,"session_id":"stub-%s","event":%q,"ts":%d}`, status, instanceID, event, at.Unix())
	require.NoError(t, os.WriteFile(filepath.Join(dir, instanceID+".json"), []byte(body), 0o600))
}

func TestIssue2853_StartAfterStopIgnoresPreviousSessionEnd(t *testing.T) {
	skipIfNoTmuxBinary(t)
	t.Setenv("TMUX", "")
	const profile = "_test-issue2853"
	d, storage := bootstrapDaemonProfile(t, profile)
	t.Cleanup(d.Flush)
	ResetInboxFingerprintCacheForTest()
	t.Cleanup(ResetInboxFingerprintCacheForTest)

	parent := NewInstanceWithTool("parent", t.TempDir(), "claude")
	parent.CreatedAt = time.Now().Add(-time.Minute)
	parent.Status = StatusIdle
	child := NewInstanceWithTool("child", t.TempDir(), "claude")
	child.CreatedAt = time.Now().Add(-time.Minute)
	child.ParentSessionID = parent.ID
	child.TmuxSocketName = "issue2853-" + child.ID
	child.tmuxSession.SocketName = child.TmuxSocketName
	t.Cleanup(func() { _ = child.Kill() })

	const stubPane = `sh -c 'printf "Claude Code stub\n> "; exec sleep 3600'`
	instances := []*Instance{parent, child}
	save := func() { require.NoError(t, storage.SaveWithGroups(instances, nil)) }

	seen := map[string]bool{}
	pass := func(when string) {
		t.Helper()
		d.syncProfile(profile)
		got := d.lastStatus[profile][child.ID]
		seen[got] = true
		require.NotEqual(t, string(StatusError), got, "daemon published error for the child (%s)", when)
		row := SavedRowStatus(storage, child.ID)
		require.NotEqual(t, string(StatusError), row, "daemon committed error to the child's row (%s)", when)
	}

	// The child is launched: first run, no hook file yet.
	require.NoError(t, child.tmuxSession.Start(stubPane))
	recordInstanceSpawn(child.ID)
	child.Status = StatusRunning
	child.markStarted()
	save()
	pass("after launch")

	for cycle := 1; cycle <= 4; cycle++ {
		if cycle > 1 {
			// session start child, as Start() orders it.
			release, err := acquireInstanceSpawnLock(child.ID)
			require.NoError(t, err)
			require.NoError(t, child.tmuxSession.Start(stubPane))
			pass(fmt.Sprintf("cycle %d: spawn in flight, row still stopped", cycle))
			recordInstanceSpawn(child.ID)
			release()
			pass(fmt.Sprintf("cycle %d: spawn stamped, row still stopped", cycle))
			child.mu.Lock()
			child.Status = StatusStarting
			child.mu.Unlock()
			child.markStarted()
			save()
			pass(fmt.Sprintf("cycle %d: start saved", cycle))
		}

		writeIssue2853Hook(t, child.ID, "running", "UserPromptSubmit", time.Now())
		pass(fmt.Sprintf("cycle %d: running", cycle))

		// The run being stopped began a minute ago (a real run is older than
		// the report's 6 s wait), so only the next spawn is newer than its
		// SessionEnd.
		stamp, err := instanceSpawnStampPath(child.ID)
		require.NoError(t, err)
		ranSince := time.Now().Add(-time.Minute)
		require.NoError(t, os.Chtimes(stamp, ranSince, ranSince))

		// session stop child (stopInstanceAndSave), then the SessionEnd hook
		// Claude emits on exit. The report waits 6 s before the next cycle;
		// the hook is dated that far back instead of sleeping.
		pre := BeforeStopKill(storage, child.ID)
		require.NoError(t, child.Kill())
		WriteStoppedAfterKill(storage, child, pre)
		save()
		writeIssue2853Hook(t, child.ID, "dead", "SessionEnd", time.Now().Add(-6*time.Second))
		pass(fmt.Sprintf("cycle %d: stopped", cycle))
		require.Equal(t, string(StatusStopped), SavedRowStatus(storage, child.ID), "cycle %d", cycle)

		// inbox drain parent: nothing urgent.
		for _, ev := range readInboxLines(t, parent.ID) {
			require.NotEqual(t, string(StatusError), normalizeStatusString(ev.ToStatus),
				"cycle %d: parent got %s -> %s for a deliberate stop", cycle, ev.FromStatus, ev.ToStatus)
		}
	}
	require.True(t, seen[string(StatusStopped)], "the daemon never observed the stops: %v", seen)
}

// A dead hook written by the CURRENT process still reads error: only a
// SessionEnd from before the newest spawn BEGAN is discarded. Two windows
// where the new process can die before Start returns (StartWithMessage keeps
// the spawn lock through message delivery, up to the agent ready timeout):
//
//	(a) the lock is still held, and the hook is dated after it was acquired;
//	(b) the lock is released and the stamp written, and the hook is dated
//	    after the spawn began but before the stamp was written.
func TestIssue2853_DeadHookAfterSpawnBeganStillReadsError(t *testing.T) {
	skipIfNoTmuxBinary(t)
	t.Setenv("TMUX", "")
	const profile = "_test-issue2853-current"
	_, storage := bootstrapDaemonProfile(t, profile)

	inst := NewInstanceWithTool("child", t.TempDir(), "claude")
	inst.CreatedAt = time.Now().Add(-time.Minute)
	inst.TmuxSocketName = "issue2853c-" + inst.ID
	inst.tmuxSession.SocketName = inst.TmuxSocketName
	t.Cleanup(func() { _ = inst.Kill() })
	inst.Status = StatusRunning
	require.NoError(t, storage.SaveWithGroups([]*Instance{inst}, nil))

	sample := func() Status {
		t.Helper()
		loaded, err := storage.Load()
		require.NoError(t, err)
		require.Len(t, loaded, 1)
		require.NoError(t, loaded[0].UpdateStatus())
		return loaded[0].GetStatusThreadSafe()
	}

	// The spawn began 3 s ago (lock acquired), and is still delivering.
	release, err := acquireInstanceSpawnLock(inst.ID)
	require.NoError(t, err)
	lockPath, err := instanceSpawnLockPath(inst.ID)
	require.NoError(t, err)
	began := time.Now().Add(-3 * time.Second)
	require.NoError(t, os.Chtimes(lockPath, began, began))
	require.NoError(t, inst.tmuxSession.Start(`sh -c 'printf "Claude Code stub\n> "; exec sleep 3600'`))

	// The new process exits 1 s after its spawn began.
	writeIssue2853Hook(t, inst.ID, "dead", "SessionEnd", began.Add(time.Second))
	require.Equal(t, StatusError, sample(), "(a) lock held, dead hook after the lock was acquired")

	// Delivery finishes: stamp written, lock released. The stamp is dated
	// to when the spawn began, not to now.
	recordInstanceSpawn(inst.ID)
	release()
	require.Equal(t, StatusError, sample(), "(b) dead hook after the spawn began, before the stamp was written")
}
