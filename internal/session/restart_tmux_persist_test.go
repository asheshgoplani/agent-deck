package session

import (
	"errors"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// restartPersistFixture opens real storage on its own profile, registers it as
// the process-wide StateDB the way main.go and NewHome do, and returns one
// saved instance whose stored tmux name is the "old" one a restart will kill.
func restartPersistFixture(t *testing.T, profile, oldName string) (*Storage, *Instance) {
	t.Helper()

	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile(%q): %v", profile, err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	db := storage.GetDB()
	if db == nil {
		t.Fatal("storage has no state database")
	}
	prev := statedb.GetGlobal()
	statedb.SetGlobal(db)
	t.Cleanup(func() { statedb.SetGlobal(prev) })

	inst := NewInstanceWithTool("restart-persist", t.TempDir(), "shell")
	inst.Status = StatusRunning
	inst.GroupPath = DefaultGroupPath
	inst.tmuxSession = tmux.ReconnectSessionLazy(oldName, inst.ID, inst.ProjectPath, "shell", "running")

	instances := []*Instance{inst}
	if err := storage.SaveWithGroups(instances, NewGroupTree(instances)); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	if got := storedTmuxName(t, storage, inst.ID); got != oldName {
		t.Fatalf("fixture: stored tmux name = %q, want %q", got, oldName)
	}
	return storage, inst
}

// storedTmuxName round-trips through storage and returns the tmux session name
// on disk for id -- what a reload, or any other process, would see.
func storedTmuxName(t *testing.T, storage *Storage, id string) string {
	t.Helper()
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	for _, inst := range instances {
		if inst.ID != id {
			continue
		}
		if sess := inst.GetTmuxSession(); sess != nil {
			return sess.Name
		}
		return ""
	}
	t.Fatalf("instance %q missing from storage", id)
	return ""
}

// TestRestartRecordsWhatItProduced drives the real Instance.Restart against
// a real tmux server (TestMain's isolated TMUX_TMPDIR keeps the host's sessions
// untouched) and asserts the name it mints reaches the state DB.
//
// This is the defect from #1870 exactly as a user hits it: restart() kills the
// old tmux session and recreateTmuxSession mints a fresh name through
// tmux.NewSession, and until this fix nothing on the CLI paths ever wrote that
// name down. The stored name kept naming the killed session, so agent-deck
// polled a session that did not exist and showed `error` for a process running
// fine, while the live tmux session was orphaned.
//
// The old name is deliberately one no tmux server has: that is what a session
// looks like after a reboot or a tmux server restart, and it is what forces
// restart() down the fallback-recreate path instead of a respawn-in-place fast
// path (which keeps the name and so needs no write).
func TestRestartRecordsWhatItProduced(t *testing.T) {
	skipIfNoTmuxBinary(t)

	const oldName = "agentdeck_restartpersist_deadbeef"
	storage, inst := restartPersistFixture(t, "_test_restart_tmux_persist", oldName)

	if err := inst.Restart(); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	t.Cleanup(func() {
		if sess := inst.GetTmuxSession(); sess != nil {
			_ = sess.Kill()
		}
	})

	live := inst.GetTmuxSession()
	if live == nil {
		t.Fatal("restart left no tmux session")
	}
	if live.Name == oldName {
		t.Fatalf("restart reused the tmux name %q; this test needs the recreate path that mints "+
			"a new one, otherwise it proves nothing", oldName)
	}

	if got := storedTmuxName(t, storage, inst.ID); got != live.Name {
		t.Errorf("stored tmux name = %q, want the minted %q: nothing recorded the restart's new "+
			"name, so agent-deck reports this live session as errored and its tmux session is "+
			"orphaned", got, live.Name)
	}

	recorded, err := inst.RestartTmuxNameRecorded()
	if !recorded || err != nil {
		t.Errorf("RestartTmuxNameRecorded() = (%v, %v), want (true, nil)", recorded, err)
	}

	// The status the restart ended in travels with the name: a row naming a
	// live pane but still marked error misreports the session just as badly.
	rows, err := storage.GetDB().LoadInstances()
	if err != nil {
		t.Fatalf("LoadInstances: %v", err)
	}
	for _, r := range rows {
		if r.ID == inst.ID && r.Status != string(inst.Status) {
			t.Errorf("stored status = %q, want %q", r.Status, inst.Status)
		}
	}

	// The write has to be identifiable as ours, or a TUI reads its own restart
	// as an external change and abandons the save carrying the rest of it.
	stamps := inst.RestartRecordStamps()
	current, err := storage.GetDB().LastModified()
	if err != nil {
		t.Fatalf("LastModified: %v", err)
	}
	if !stamps.SoleWriterSince(stamps.Before, current) {
		t.Errorf("restart write is not recognisable as our own: stamps=%+v current=%d", stamps, current)
	}
}

// TestRestartReportsAnUnrecordedTmuxName pins the honesty half. If the write
// cannot land -- here because the row was deleted while the restart ran -- the
// restart still succeeded and the process is live, so Restart must not fail.
// It must, however, leave the caller able to say the outcome is unknown rather
// than print a plain success for a session agent-deck can no longer find.
func TestRestartReportsAnUnrecordedTmuxName(t *testing.T) {
	skipIfNoTmuxBinary(t)

	const oldName = "agentdeck_restartunrecorded_deadbeef"
	storage, inst := restartPersistFixture(t, "_test_restart_tmux_unrecorded", oldName)

	// Another process removes the session while this one is restarting it.
	if err := storage.GetDB().DeleteInstance(inst.ID); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}

	if err := inst.Restart(); err != nil {
		t.Fatalf("Restart returned %v; a restart whose bookkeeping failed still started the "+
			"process, and reporting it as failed invites a retry that leaks another tmux session", err)
	}
	t.Cleanup(func() {
		if sess := inst.GetTmuxSession(); sess != nil {
			_ = sess.Kill()
		}
	})

	recorded, err := inst.RestartTmuxNameRecorded()
	if recorded {
		t.Fatal("RestartTmuxNameRecorded() = true after the row was deleted: the caller would " +
			"report a durable restart that was never recorded")
	}
	if !errors.Is(err, statedb.ErrInstanceNotStored) {
		t.Errorf("reason = %v, want it to wrap ErrInstanceNotStored so the message names the cause", err)
	}
}

// TestRestartTmuxNameRecordedDefaultsToRecorded keeps the accessor from
// reporting a problem on an instance that has never restarted -- callers use it
// to decide whether to warn, and a warning about a restart that did not happen
// is noise.
func TestRestartTmuxNameRecordedDefaultsToRecorded(t *testing.T) {
	inst := NewInstanceWithTool("never-restarted", t.TempDir(), "shell")
	inst.CreatedAt = time.Now()

	recorded, err := inst.RestartTmuxNameRecorded()
	if !recorded || err != nil {
		t.Errorf("RestartTmuxNameRecorded() = (%v, %v) before any restart, want (true, nil)", recorded, err)
	}
}
