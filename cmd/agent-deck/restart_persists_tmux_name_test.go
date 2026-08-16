// Regression guards for the CLI restart paths that recreate a tmux session.
//
// Instance.Restart recreates the tmux session under a NEW name, and that name
// is the only handle anything has on the live process. These CLI commands never
// recorded it, so the stored tmux_session column kept naming the killed
// session: agent-deck reported the session as errored while its process ran
// fine, and the live tmux session was orphaned because nothing knew its name
// (#1870).
//
// These tests drive the real command helper -- the same call `skill attach
// --restart` and `skill detach --restart` make -- against a real tmux server on
// the package's isolated socket, rather than swapping the tmux pointer by hand
// and calling a save helper the commands no longer use.

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// skipIfNoTmuxBinaryCLI skips when tmux is absent; these tests drive a real
// restart on the package's isolated tmux socket.
func skipIfNoTmuxBinaryCLI(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
}

// TestSkillRestartCommandPath_RecordsNewTmuxName is the regression guard. It
// asserts the contract that matters: after the command path restarts a session,
// the tmux name that survives a round-trip through storage is the new one.
func TestSkillRestartCommandPath_RecordsNewTmuxName(t *testing.T) {
	skipIfNoTmuxBinaryCLI(t)

	storage, inst := newCLIRestartFixture(t, "_test_cli_restart_records")

	// Exactly what handleSkillAttach/handleSkillDetach do before restarting.
	adoptStateDB(storage)
	outcome := restartProjectSkillsSession(inst, false, true)
	t.Cleanup(func() {
		if sess := inst.GetTmuxSession(); sess != nil {
			_ = sess.Kill()
		}
	})

	if !outcome.Restarted {
		t.Fatal("command path reported no restart for a restart-eligible session")
	}
	live := inst.GetTmuxSession()
	if live == nil {
		t.Fatal("restart left no tmux session")
	}
	if live.Name == cliRestartOldTmuxName {
		t.Fatalf("restart reused %q instead of minting a new name; this test would prove nothing", cliRestartOldTmuxName)
	}

	if got := storedTmuxNameCLI(t, storage, inst.ID); got != live.Name {
		t.Errorf("stored tmux name = %q, want the minted %q: the command path left the killed "+
			"session's name on disk, so agent-deck shows this live session as errored and its "+
			"tmux session is orphaned", got, live.Name)
	}
	if !outcome.Recorded {
		t.Errorf("outcome.Recorded = false (%s) even though the name reached storage", outcome.Reason)
	}
}

// TestSkillRestartCommandPath_ReportsUnrecordedRestart pins the honesty half.
// The row is deleted while the restart runs, as a peer process removing the
// session would do. The restart still succeeded, so the command must not claim
// failure -- but it must not print plain success either, because agent-deck can
// no longer find the session it just started.
func TestSkillRestartCommandPath_ReportsUnrecordedRestart(t *testing.T) {
	skipIfNoTmuxBinaryCLI(t)

	storage, inst := newCLIRestartFixture(t, "_test_cli_restart_unrecorded")
	if err := storage.GetDB().DeleteInstance(inst.ID); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}

	adoptStateDB(storage)
	outcome := restartProjectSkillsSession(inst, false, true)
	t.Cleanup(func() {
		if sess := inst.GetTmuxSession(); sess != nil {
			_ = sess.Kill()
		}
	})

	if !outcome.Restarted {
		t.Fatal("command path reported no restart; the process did start")
	}
	if outcome.Recorded {
		t.Fatal("outcome.Recorded = true after the row vanished: the command would report a " +
			"durable restart that was never recorded")
	}
	if outcome.Reason == "" {
		t.Error("outcome.Reason is empty: the operator gets a warning with no cause in it")
	}

	// The JSON both skill commands emit has to carry the distinction, not just
	// the human message -- a script reading `restarted: true` would otherwise
	// conclude everything is fine.
	payload := map[string]interface{}{"success": true}
	outcome.addTo(payload)
	if payload["restarted"] != true {
		t.Errorf("payload[restarted] = %v, want true", payload["restarted"])
	}
	if payload["tmux_name_recorded"] != false {
		t.Errorf("payload[tmux_name_recorded] = %v, want false", payload["tmux_name_recorded"])
	}
	if payload["restart_warning"] == nil {
		t.Error("payload has no restart_warning explaining the unknown outcome")
	}
}

// TestRestartOutcome_NoRestartRequestedStaysQuiet keeps the reporting from
// crying wolf: a command run without --restart has nothing to record, and a
// blanket tmux_name_recorded=false there would read as a failure.
func TestRestartOutcome_NoRestartRequestedStaysQuiet(t *testing.T) {
	outcome := restartOutcomeFor(nil, false)

	payload := map[string]interface{}{}
	outcome.addTo(payload)
	if payload["restarted"] != false {
		t.Errorf("payload[restarted] = %v, want false", payload["restarted"])
	}
	if _, ok := payload["tmux_name_recorded"]; ok {
		t.Error("payload carries tmux_name_recorded with no restart requested")
	}
	if got := outcome.describe("Attached x to y"); got != "Attached x to y" {
		t.Errorf("describe() = %q, want the message unchanged", got)
	}
}

const cliRestartOldTmuxName = "agentdeck_clirestart_deadbeef"

// newCLIRestartFixture returns storage on its own profile plus one saved,
// restart-eligible session whose stored tmux session is dead -- what a session
// looks like after a reboot, and what forces restart() down the path that mints
// a new name instead of respawning in place.
func newCLIRestartFixture(t *testing.T, profile string) (*session.Storage, *session.Instance) {
	t.Helper()

	// A codex session is restart-eligible for skills (ShouldRestartProjectSkills)
	// and, unlike claude, skips the two-second "continue" nudge.
	stubDir := t.TempDir()
	stub := filepath.Join(stubDir, "codex")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatalf("write codex stub: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	prev := statedb.GetGlobal()
	statedb.SetGlobal(nil)
	t.Cleanup(func() { statedb.SetGlobal(prev) })

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile(%q): %v", profile, err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	inst := session.NewInstanceWithTool("cli-restart-target", t.TempDir(), "codex")
	inst.Status = session.StatusRunning
	inst.GroupPath = session.DefaultGroupPath
	inst.SetTmuxSessionForTest(tmux.ReconnectSessionLazy(
		cliRestartOldTmuxName, inst.ID, inst.ProjectPath, "codex", "running",
	))

	instances := []*session.Instance{inst}
	if err := saveSessionData(storage, instances, nil); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	if got := storedTmuxNameCLI(t, storage, inst.ID); got != cliRestartOldTmuxName {
		t.Fatalf("fixture: stored tmux name = %q, want %q", got, cliRestartOldTmuxName)
	}
	return storage, inst
}

// storedTmuxNameCLI round-trips through storage and returns the tmux session
// name on disk for id -- what any other process would see.
func storedTmuxNameCLI(t *testing.T, storage *session.Storage, id string) string {
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
	t.Fatalf("session %q missing from storage", id)
	return ""
}
