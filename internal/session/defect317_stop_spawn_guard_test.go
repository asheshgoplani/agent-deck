package session

import (
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Defect 317, stale-name window: a recreate-path restart spawns under a new
// tmux name that reaches the row only at its later full save. Until then the
// row and the stop's instance hold the old, dead name, and a restarting
// process may already have written running. The spawn guard (lock held, or
// generation moved since the stop's pre-kill read) must keep the stop from
// writing stopped over it.

func defect317SpawnGuardStop(t *testing.T, during func(id string)) string {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	prev := agentDeckDirOverride
	agentDeckDirOverride = filepath.Join(home, ".agent-deck")
	t.Cleanup(func() { agentDeckDirOverride = prev })

	inst := NewInstance("spawnguard", t.TempDir())
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
	inst.Status = StatusIdle
	storage, err := NewStorageWithProfile("_defect317_spawnguard")
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatal(err)
	}

	pre := BeforeStopKill(storage, inst.ID)
	if pre.Status != string(StatusIdle) {
		t.Fatalf("pre-kill status = %q, want idle", pre.Status)
	}
	if err := inst.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	StopAfterKillHook = func(id string) {
		during(id)
		if err := storage.GetDB().WriteStatus(id, string(StatusRunning), inst.Tool); err != nil {
			t.Errorf("concurrent write: %v", err)
		}
	}
	t.Cleanup(func() { StopAfterKillHook = nil })
	WriteStoppedAfterKill(storage, inst, pre)
	row, err := storage.GetDB().LoadInstanceByID(inst.ID)
	if err != nil || row == nil {
		t.Fatalf("load row: %v", err)
	}
	return row.Status
}

func TestDefect317_StopDoesNotOverwriteWhileASpawnHoldsTheLock(t *testing.T) {
	var release func()
	t.Cleanup(func() {
		if release != nil {
			release()
		}
	})
	got := defect317SpawnGuardStop(t, func(id string) {
		r, err := acquireInstanceSpawnLock(id)
		if err != nil {
			t.Fatalf("acquire spawn lock: %v", err)
		}
		release = r
	})
	if got != string(StatusRunning) {
		t.Fatalf("row status = %q, want the in-flight restart's running", got)
	}
}

func TestDefect317_StopDoesNotOverwriteAfterASpawnSinceTheKill(t *testing.T) {
	got := defect317SpawnGuardStop(t, func(id string) { recordInstanceSpawn(id) })
	if got != string(StatusRunning) {
		t.Fatalf("row status = %q, want the restart's running", got)
	}
}

// Control: no spawn, pane gone: the running sample is the field case and the
// stop writes stopped.
func TestDefect317_StopOverwritesARunningSampleWithNoSpawn(t *testing.T) {
	got := defect317SpawnGuardStop(t, func(string) {})
	if got != string(StatusStopped) {
		t.Fatalf("row status = %q, want stopped", got)
	}
}
