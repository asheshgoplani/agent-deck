package session

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// A loaded instance must write restart bookkeeping to its own profile even
// when another profile is registered as the process-global database.
func TestRestartOutcomeUsesOwningProfile(t *testing.T) {
	skipIfNoTmuxBinary(t)

	suffix := randomString(8)
	storage, _ := restartPersistFixture(t, "_test_restart_owner_"+suffix, "agentdeck_restartprofile_deadbeef")
	instances, _, err := storage.LoadWithGroups()
	if err != nil || len(instances) != 1 {
		t.Fatalf("reload owning profile: instances=%d err=%v", len(instances), err)
	}
	inst := instances[0]

	other, err := NewStorageWithProfile("_test_restart_other_" + suffix)
	if err != nil {
		t.Fatalf("NewStorageWithProfile(other): %v", err)
	}
	t.Cleanup(func() { _ = other.Close() })
	previous := statedb.GetGlobal()
	statedb.SetGlobal(other.GetDB())
	t.Cleanup(func() { statedb.SetGlobal(previous) })

	if err := inst.Restart(); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	t.Cleanup(func() {
		if sess := inst.GetTmuxSession(); sess != nil {
			_ = sess.Kill()
		}
	})

	if recorded, err := inst.RestartTmuxNameRecorded(); !recorded || err != nil {
		t.Fatalf("RestartTmuxNameRecorded() = (%v, %v), want owning-profile success", recorded, err)
	}
	live := inst.GetTmuxSession()
	if live == nil || storedTmuxName(t, storage, inst.ID) != live.Name {
		t.Fatalf("owning profile did not retain the restarted runtime: live=%v", live)
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

// TestRestartKeepsLiveBindingWhenSpawnIsKilled is the regression for the
// ghost-binding cascade: recreateTmuxSession mints a new tmux name BEFORE the
// spawn proves it exists, so when the `tmux new-session` child is killed
// externally (here: a shim that self-SIGKILLs on new-session, mirroring a
// security-policy kill) the instance used to keep the name nothing answers on
// — while the previous pane was still alive. Every later save then persisted
// the dead name and the live session became an orphan no TUI could attach.
// The restart must fall back to the still-live binding instead.
func TestRestartKeepsLiveBindingWhenSpawnIsKilled(t *testing.T) {
	skipIfNoTmuxBinary(t)

	const oldName = "agentdeck_restartbinding_deadbeef"
	storage, inst := restartPersistFixture(t, "_test_restart_binding_restore", oldName)

	// The "previous" session is a real tmux session on the isolated socket,
	// created with the REAL tmux binary before the shim goes on PATH.
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skipf("tmux not on PATH: %v", err)
	}
	if out, err := exec.Command(realTmux, "new-session", "-d", "-s", oldName, "sleep 60").CombinedOutput(); err != nil {
		t.Fatalf("seed old session: %v: %s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command(realTmux, "kill-session", "-t", oldName).Run()
	})

	// Every tmux invocation during the restart goes through this shim: the
	// subprocess the spawn pipeline execs dies by signal (exactly what the
	// incident's probes saw), while everything else still reaches the real
	// server so has-session/display-message answer truthfully.
	shimDir := t.TempDir()
	shim := filepath.Join(shimDir, "tmux")
	script := "#!/bin/sh\ncase \" $* \" in\n" +
		"  *\" new-session \"*|*\" kill-session \"*) kill -9 $$ ;;\n" +
		"esac\nexec " + realTmux + " \"$@\"\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatalf("write tmux shim: %v", err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err = inst.Restart()
	if err == nil {
		t.Fatal("restart must fail when its new-session child is killed")
	}

	live := inst.GetTmuxSession()
	if live == nil || live.Name != oldName {
		name := "<nil>"
		if live != nil {
			name = live.Name
		}
		t.Fatalf("binding after failed restart = %q, want the still-live %q", name, oldName)
	}
	if inst.Status != StatusError {
		t.Fatalf("status = %q, want error (the restart did fail)", inst.Status)
	}

	// The next save must still record the live name — persisting the minted
	// one here is exactly how the incident left ghost rows in state.db.
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTree([]*Instance{inst})); err != nil {
		t.Fatalf("save after failed restart: %v", err)
	}
	if got := storedTmuxName(t, storage, inst.ID); got != oldName {
		t.Errorf("stored tmux name = %q, want %q: the failed spawn's minted name leaked to disk", got, oldName)
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

// restartBindingShim puts a tmux shim first on PATH that SIGKILLs itself for
// every invocation matching one of the given shell case patterns (matched
// against " $* ") and hands everything else to the real binary. A
// signal-killed client is exactly the indeterminate outcome the restore
// verdict has to reason about.
func restartBindingShim(t *testing.T, realTmux string, killPatterns ...string) {
	t.Helper()
	script := "#!/bin/sh\ncase \" $* \" in\n"
	for _, p := range killPatterns {
		script += "  " + p + ") kill -9 $$ ;;\n"
	}
	script += "esac\nexec " + realTmux + " \"$@\"\n"
	shimDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(shimDir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatalf("write tmux shim: %v", err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// seedRealTmuxSession starts a detached session with the real binary and
// returns the tmux path.
func seedRealTmuxSession(t *testing.T, name string) string {
	t.Helper()
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skipf("tmux not on PATH: %v", err)
	}
	if out, err := exec.Command(realTmux, "new-session", "-d", "-s", name, "sleep 60").CombinedOutput(); err != nil {
		t.Fatalf("seed old session %q: %v: %s", name, err, out)
	}
	t.Cleanup(func() { _ = exec.Command(realTmux, "kill-session", "-t", "="+name).Run() })
	return realTmux
}

// killSessionsCreatedDuring snapshots the server's sessions now and, at
// cleanup, kills any session that was not there before, so a replacement the
// restart created cannot leak whichever binding the instance ends up with.
func killSessionsCreatedDuring(t *testing.T, realTmux string) {
	t.Helper()
	list := func() map[string]bool {
		out, _ := exec.Command(realTmux, "list-sessions", "-F", "#{session_name}").Output()
		names := map[string]bool{}
		for _, n := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if n != "" {
				names[n] = true
			}
		}
		return names
	}
	before := list()
	t.Cleanup(func() {
		for n := range list() {
			if !before[n] {
				_ = exec.Command(realTmux, "kill-session", "-t", "="+n).Run()
			}
		}
	})
}

func realTmuxHasSession(realTmux, name string) bool {
	return exec.Command(realTmux, "has-session", "-t", "="+name).Run() == nil
}

// TestRestartDoesNotRebindToKilledSessionWhenReplacementStarted covers the
// case the restore must not touch: the old session was killed successfully,
// the replacement tmux session was created, and only a later Start() step
// (here the send-keys for a shell session's command) failed. An inconclusive
// probe of the old name at that moment must not rebind the instance to the
// session we just killed: that would leave the running replacement pane with
// nothing pointing at it.
func TestRestartDoesNotRebindToKilledSessionWhenReplacementStarted(t *testing.T) {
	skipIfNoTmuxBinary(t)

	const oldName = "agentdeck_restartbinding_killedok"
	_, inst := restartPersistFixture(t, "_test_restart_binding_killed_ok", oldName)
	// A shell session with a command takes Start()'s send-keys fallback, a
	// step that runs only after new-session has created the replacement.
	inst.Command = "sleep 60"
	realTmux := seedRealTmuxSession(t, oldName)
	killSessionsCreatedDuring(t, realTmux)

	// kill-session reaches the real server (the kill succeeds); every
	// has-session aimed at the old name is killed (indeterminate); send-keys
	// is killed so Start() fails after new-session already succeeded.
	restartBindingShim(t, realTmux,
		`*" has-session "*`+oldName+`*`,
		`*" send-keys "*`,
		`*" load-buffer "*`,
		`*" paste-buffer "*`,
	)

	if err := inst.Restart(); err == nil {
		t.Fatal("restart must fail when its send-keys step is killed")
	}

	if realTmuxHasSession(realTmux, oldName) {
		t.Fatalf("fixture: old session %q survived; this test needs the kill to succeed", oldName)
	}
	live := inst.GetTmuxSession()
	if live == nil {
		t.Fatal("failed restart left no tmux binding")
	}
	if live.Name == oldName {
		t.Fatalf("binding after failed restart = %q: rebound to the session restart just killed, "+
			"orphaning the replacement that is running", oldName)
	}
	if !realTmuxHasSession(realTmux, live.Name) {
		t.Fatalf("binding %q names no live session; want the replacement restart created", live.Name)
	}
}

// TestRestartRestoresLiveBindingOnIndeterminateProbe: the kill of the old
// session did not succeed and the replacement never came up. The old name's
// probe is inconclusive at failure time, which must still restore the old
// binding (the pane is in fact alive).
func TestRestartRestoresLiveBindingOnIndeterminateProbe(t *testing.T) {
	skipIfNoTmuxBinary(t)

	const oldName = "agentdeck_restartbinding_indeterminate"
	_, inst := restartPersistFixture(t, "_test_restart_binding_indeterminate", oldName)
	realTmux := seedRealTmuxSession(t, oldName)
	killSessionsCreatedDuring(t, realTmux)

	restartBindingShim(t, realTmux,
		`*" has-session "*`+oldName+`*`,
		`*" kill-session "*`,
		`*" new-session "*`,
	)

	if err := inst.Restart(); err == nil {
		t.Fatal("restart must fail when its new-session child is killed")
	}

	if !realTmuxHasSession(realTmux, oldName) {
		t.Fatalf("fixture: old session %q is gone; this test needs it alive", oldName)
	}
	live := inst.GetTmuxSession()
	if live == nil || live.Name != oldName {
		name := "<nil>"
		if live != nil {
			name = live.Name
		}
		t.Fatalf("binding after failed restart = %q, want the still-live %q", name, oldName)
	}
}

// TestRestartDoesNotRestoreAuthoritativelyAbsentBinding: the old name is
// provably gone (a completed has-session says "can't find session"), so a
// failed spawn must not point the instance back at it.
func TestRestartDoesNotRestoreAuthoritativelyAbsentBinding(t *testing.T) {
	skipIfNoTmuxBinary(t)

	const oldName = "agentdeck_restartbinding_absent"
	_, inst := restartPersistFixture(t, "_test_restart_binding_absent", oldName)
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skipf("tmux not on PATH: %v", err)
	}
	if realTmuxHasSession(realTmux, oldName) {
		t.Fatalf("fixture: %q unexpectedly exists", oldName)
	}
	killSessionsCreatedDuring(t, realTmux)

	restartBindingShim(t, realTmux, `*" new-session "*`)

	if err := inst.Restart(); err == nil {
		t.Fatal("restart must fail when its new-session child is killed")
	}

	if live := inst.GetTmuxSession(); live != nil && live.Name == oldName {
		t.Fatalf("binding after failed restart = %q: restored a session a completed probe proved gone", oldName)
	}
}

// TestRestartKeepsReplacementBindingWhenItExists: the old session's kill
// failed and it is still alive, but the replacement was created before a
// later Start() step failed. The replacement is the session the user asked
// for, so the binding must stay on it rather than go back to the old one.
func TestRestartKeepsReplacementBindingWhenItExists(t *testing.T) {
	skipIfNoTmuxBinary(t)

	const oldName = "agentdeck_restartbinding_replacement"
	_, inst := restartPersistFixture(t, "_test_restart_binding_replacement", oldName)
	inst.Command = "sleep 60"
	realTmux := seedRealTmuxSession(t, oldName)
	killSessionsCreatedDuring(t, realTmux)

	restartBindingShim(t, realTmux,
		`*" kill-session "*`,
		`*" send-keys "*`,
		`*" load-buffer "*`,
		`*" paste-buffer "*`,
	)

	if err := inst.Restart(); err == nil {
		t.Fatal("restart must fail when its send-keys step is killed")
	}

	if !realTmuxHasSession(realTmux, oldName) {
		t.Fatalf("fixture: old session %q is gone; this test needs the kill to fail", oldName)
	}
	live := inst.GetTmuxSession()
	if live == nil || live.Name == oldName {
		t.Fatalf("binding after failed restart went back to %q although the replacement exists", oldName)
	}
	if !realTmuxHasSession(realTmux, live.Name) {
		t.Fatalf("binding %q names no live session; want the replacement restart created", live.Name)
	}
}
