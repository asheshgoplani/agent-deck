package core

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Defect 317 on the default stop path (`agent-deck session stop` and the
// daemon route): another process that samples the pane between the kill and
// the save commits error. The stop must still succeed and save stopped, or
// the idle-death watch (#2806) tells the parent its child died. A concurrent
// running sample of the dead pane (a child waking, or the dying process's own
// hooks, inside the kill window) is not a restart either. A restart is a live
// pane for the session after the kill, and is not overwritten.

func stopRaceSession(t *testing.T) (profile string, inst *session.Instance) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	profile = fmt.Sprintf("_core_stop_race_%d", time.Now().UnixNano())
	inst = session.NewInstance(fmt.Sprintf("stoprace-%d", time.Now().UnixNano()), t.TempDir())
	inst.Command = ""
	if err := inst.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = inst.Kill() })
	deadline := time.Now().Add(3 * time.Second)
	for !inst.Exists() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !inst.Exists() {
		t.Fatal("tmux session never appeared")
	}
	inst.Status = session.StatusIdle
	seedStore(t, profile, nil, inst)
	return profile, inst
}

// stopRaceStop runs session.stop with status committed by "another process"
// between the kill and the save, and returns the error and the saved status.
// With restart, the hook first respawns a live tmux session under the
// instance's own tmux name and socket, as a restart would.
func stopRaceStop(t *testing.T, profile string, inst *session.Instance, status string, restart bool) (error, string) {
	t.Helper()
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	session.StopAfterKillHook = func(id string) {
		if restart {
			stopRaceRespawnPane(t, inst)
		}
		if err := storage.GetDB().WriteStatus(id, status, inst.Tool); err != nil {
			t.Errorf("concurrent write: %v", err)
		}
	}
	t.Cleanup(func() { session.StopAfterKillHook = nil })
	_, res := Invoke[SessionStopOut](context.Background(), testRegistry(t, Deps{}), IDSessionStop, SessionStopIn{Profile: profile, Session: inst.Title})
	row, err := storage.GetDB().LoadInstanceByID(inst.ID)
	if err != nil || row == nil {
		t.Fatalf("load row: %v", err)
	}
	return res.Err, row.Status
}

// stopRaceRespawnPane starts a live tmux session under inst's tmux name on
// its socket (the test's isolated server).
func stopRaceRespawnPane(t *testing.T, inst *session.Instance) {
	t.Helper()
	ts := inst.GetTmuxSession()
	if ts == nil {
		t.Fatal("instance has no tmux session")
	}
	var args []string
	if ts.SocketName != "" {
		args = append(args, "-L", ts.SocketName)
	}
	name := ts.TmuxName()
	if out, err := exec.Command("tmux", append(args, "new-session", "-d", "-s", name, "sleep 600")...).CombinedOutput(); err != nil {
		t.Fatalf("respawn tmux session %q: %v: %s", name, err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", append(args, "kill-session", "-t", "="+name)...).Run() })
}

func TestSessionStopWinsOverAConcurrentErrorSample(t *testing.T) {
	profile, inst := stopRaceSession(t)
	err, status := stopRaceStop(t, profile, inst, string(session.StatusError), false)
	if err != nil {
		t.Fatalf("stop refused by a concurrent error sample of its own kill: %v", err)
	}
	if status != string(session.StatusStopped) {
		t.Fatalf("saved status = %q, want stopped", status)
	}
}

// The field failure (deckbox, rc.3): running committed inside the kill
// window, with no pane left alive. The stop must still succeed.
func TestSessionStopWinsOverAConcurrentRunningSampleOfADeadPane(t *testing.T) {
	profile, inst := stopRaceSession(t)
	err, status := stopRaceStop(t, profile, inst, string(session.StatusRunning), false)
	if err != nil {
		t.Fatalf("stop refused by a running sample of its own dead pane: %v", err)
	}
	if status != string(session.StatusStopped) {
		t.Fatalf("saved status = %q, want stopped", status)
	}
}

func TestSessionStopDoesNotOverwriteAConcurrentRestart(t *testing.T) {
	for _, restart := range []session.Status{session.StatusStarting, session.StatusRunning} {
		t.Run(string(restart), func(t *testing.T) {
			profile, inst := stopRaceSession(t)
			err, status := stopRaceStop(t, profile, inst, string(restart), true)
			if err == nil || !strings.Contains(err.Error(), "stale concurrent Status conflict") {
				t.Fatalf("stop over a concurrent restart: err = %v, want a Status conflict", err)
			}
			if status != string(restart) {
				t.Fatalf("saved status = %q, want the restart's %s", status, restart)
			}
		})
	}
}

// A recreate-path restart spawns under a NEW tmux name and records it in the
// row, while the stop's instance still holds the old, dead name. The live
// pane under the row's name is a restart: no overwrite, the save conflicts.
func TestSessionStopDoesNotOverwriteARestartUnderANewTmuxName(t *testing.T) {
	profile, inst := stopRaceSession(t)
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	ts := inst.GetTmuxSession()
	var args []string
	if ts.SocketName != "" {
		args = append(args, "-L", ts.SocketName)
	}
	newName := ts.TmuxName() + "-recreated"
	session.StopAfterKillHook = func(id string) {
		if out, err := exec.Command("tmux", append(args, "new-session", "-d", "-s", newName, "sleep 600")...).CombinedOutput(); err != nil {
			t.Errorf("spawn %q: %v: %s", newName, err, out)
			return
		}
		if _, err := storage.GetDB().DB().Exec(`UPDATE instances SET tmux_session = ? WHERE id = ?`, newName, id); err != nil {
			t.Errorf("record new tmux name: %v", err)
		}
		if err := storage.GetDB().WriteStatus(id, string(session.StatusRunning), inst.Tool); err != nil {
			t.Errorf("concurrent write: %v", err)
		}
	}
	t.Cleanup(func() {
		session.StopAfterKillHook = nil
		_ = exec.Command("tmux", append(args, "kill-session", "-t", "="+newName)...).Run()
	})
	_, res := Invoke[SessionStopOut](context.Background(), testRegistry(t, Deps{}), IDSessionStop, SessionStopIn{Profile: profile, Session: inst.Title})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "stale concurrent Status conflict") {
		t.Fatalf("stop over a restart under a new tmux name: err = %v, want a Status conflict", res.Err)
	}
	row, err := storage.GetDB().LoadInstanceByID(inst.ID)
	if err != nil || row == nil {
		t.Fatalf("load row: %v", err)
	}
	if row.Status != string(session.StatusRunning) {
		t.Fatalf("saved status = %q, want the restart's running", row.Status)
	}
}
