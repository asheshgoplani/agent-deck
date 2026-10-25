package main

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Defect 317: `session stop` kills the pane and then saves stopped. Another
// process (the notify daemon, a TUI) that samples the pane in between sees it
// gone and commits error. That refused the stop's save (stale concurrent
// Status conflict), the stop exited 1, the row stayed error, and the
// idle-death watch (#2806) told the parent its child had died.
//
// session.StopAfterKillHook commits the other writer's status between the kill and
// the saves, in process, against a real tmux session.

func defect317StartedSession(t *testing.T, title string) (home, id string) {
	t.Helper()
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home = t.TempDir()
	id = addExitStatusSession(t, home, title, "#!/bin/sh\nsleep 600\n")
	stdout, stderr, code := runAgentDeck(t, home, "-p", "_test", "session", "start", id)
	require.Zero(t, code, "%s %s", stdout, stderr)
	t.Setenv("HOME", home)
	t.Setenv("AGENTDECK_PROFILE", "_test")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	return home, id
}

// defect317Stop runs the legacy stop path (AGENTDECK_CORE_REGISTRY=0, and
// `session command /exit`) in process with hook between kill and save. The
// default core path is covered in internal/core.
func defect317Stop(t *testing.T, id string, hook func(storage *session.Storage, id string)) error {
	t.Helper()
	storage, instances, groups, err := loadSessionData("_test")
	require.NoError(t, err)
	defer storage.Close()
	inst, msg, _ := ResolveSession(id, instances)
	require.NotNil(t, inst, msg)
	require.True(t, inst.Exists(), "the session must be running before the stop")
	if hook != nil {
		session.StopAfterKillHook = func(id string) { hook(storage, id) }
		t.Cleanup(func() { session.StopAfterKillHook = nil })
	}
	_, err = stopInstanceAndSave(storage, inst, instances, groups)
	return err
}

func TestDefect317_StopWinsOverAConcurrentErrorSample(t *testing.T) {
	_, id := defect317StartedSession(t, "stop317-error")
	err := defect317Stop(t, id, func(storage *session.Storage, id string) {
		require.NoError(t, storage.GetDB().WriteStatus(id, string(session.StatusError), "shell"))
	})
	require.NoError(t, err, "a concurrent error sample of the stop's own kill must not refuse the stop")
	require.Equal(t, string(session.StatusStopped), issue2806RowStatus(t, id))
}

func TestDefect317_StopWithoutARaceSavesStopped(t *testing.T) {
	_, id := defect317StartedSession(t, "stop317-plain")
	require.NoError(t, defect317Stop(t, id, nil))
	require.Equal(t, string(session.StatusStopped), issue2806RowStatus(t, id))
}

// The field failure (deckbox, rc.3): running committed inside the kill
// window (a child waking, or the dying process's own hooks), with no pane left
// alive. That is not a restart: the stop must succeed and save stopped.
func TestDefect317_StopWinsOverAConcurrentRunningSampleOfADeadPane(t *testing.T) {
	_, id := defect317StartedSession(t, "stop317-running")
	err := defect317Stop(t, id, func(storage *session.Storage, id string) {
		require.NoError(t, storage.GetDB().WriteStatus(id, string(session.StatusRunning), "shell"))
	})
	require.NoError(t, err, "a running sample of the stop's own dead pane must not refuse the stop")
	require.Equal(t, string(session.StatusStopped), issue2806RowStatus(t, id))
}

// defect317RespawnPane starts a live tmux session under the row's tmux name
// and socket (the test's isolated server), as a restart would.
func defect317RespawnPane(t *testing.T, storage *session.Storage, id string) {
	t.Helper()
	row, err := storage.GetDB().LoadInstanceByID(id)
	require.NoError(t, err)
	require.NotNil(t, row)
	require.NotEmpty(t, row.TmuxSession)
	var args []string
	if row.TmuxSocketName != "" {
		args = append(args, "-L", row.TmuxSocketName)
	}
	out, err := exec.Command("tmux", append(args, "new-session", "-d", "-s", row.TmuxSession, "sleep 600")...).CombinedOutput()
	require.NoError(t, err, "respawn tmux session %q: %s", row.TmuxSession, out)
	t.Cleanup(func() { _ = exec.Command("tmux", append(args, "kill-session", "-t", "="+row.TmuxSession)...).Run() })
}

// A restart is a live pane for the session after the kill, newer than the
// stop's view: the stop does not overwrite its status, and its save still
// conflicts.
func TestDefect317_StopDoesNotOverwriteAConcurrentRestart(t *testing.T) {
	_, id := defect317StartedSession(t, "stop317-restart")
	// The restart's status must differ from the one the stop loaded (a fresh
	// session's row can still read starting).
	restart := string(session.StatusStarting)
	if issue2806RowStatus(t, id) == restart {
		restart = string(session.StatusRunning)
	}
	err := defect317Stop(t, id, func(storage *session.Storage, id string) {
		defect317RespawnPane(t, storage, id)
		require.NoError(t, storage.GetDB().WriteStatus(id, restart, "shell"))
	})
	require.ErrorContains(t, err, "stale concurrent Status conflict")
	require.Equal(t, restart, issue2806RowStatus(t, id))
}
