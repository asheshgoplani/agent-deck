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
// starting is a restart and must not be overwritten.

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
func stopRaceStop(t *testing.T, profile string, inst *session.Instance, status string) (error, string) {
	t.Helper()
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	session.StopAfterKillHook = func(id string) {
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

func TestSessionStopWinsOverAConcurrentErrorSample(t *testing.T) {
	profile, inst := stopRaceSession(t)
	err, status := stopRaceStop(t, profile, inst, string(session.StatusError))
	if err != nil {
		t.Fatalf("stop refused by a concurrent error sample of its own kill: %v", err)
	}
	if status != string(session.StatusStopped) {
		t.Fatalf("saved status = %q, want stopped", status)
	}
}

func TestSessionStopDoesNotOverwriteAConcurrentRestart(t *testing.T) {
	profile, inst := stopRaceSession(t)
	err, status := stopRaceStop(t, profile, inst, string(session.StatusStarting))
	if err == nil || !strings.Contains(err.Error(), "stale concurrent Status conflict") {
		t.Fatalf("stop over a concurrent restart: err = %v, want a Status conflict", err)
	}
	if status != string(session.StatusStarting) {
		t.Fatalf("saved status = %q, want the restart's starting", status)
	}
}
