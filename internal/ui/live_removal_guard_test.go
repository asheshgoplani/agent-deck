package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestRemoveSessionBlocksLiveTmuxDespiteStoppedStatus(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	inst := session.NewInstance("live-removal-guard", t.TempDir())
	inst.Status = session.StatusStopped // stale persisted status: the reported failure shape
	tmuxSess := inst.GetTmuxSession()
	if err := tmuxSess.Start("sleep 60"); err != nil {
		t.Skipf("tmux unavailable: %v", err)
	}
	t.Cleanup(func() { _ = tmuxSess.Kill() })

	msg := (&Home{}).removeSession(inst)()
	if _, deleted := msg.(sessionDeletedMsg); deleted {
		t.Fatal("registry-only removal deleted a session whose tmux process is still live")
	}
}

func TestRegistryRemovalAbortsWhenQueueTransactionCannotStart(t *testing.T) {
	dataRoot := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataRoot)
	runtimeDir := filepath.Join(dataRoot, "agent-deck", "runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(runtimeDir, "runtime-queue-locks")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	inst := session.NewInstance("discard-failure", t.TempDir())
	inst.Status = session.StatusStopped
	msg := registryRemovalMsg(inst)
	if _, ok := msg.(sessionDeleteFailedMsg); !ok {
		t.Fatalf("registryRemovalMsg() = %T, want sessionDeleteFailedMsg", msg)
	}
}

func TestDeleteSessionAbortsWhenQueueTransactionCannotStart(t *testing.T) {
	dataRoot := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataRoot)
	runtimeDir := filepath.Join(dataRoot, "agent-deck", "runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(runtimeDir, "runtime-queue-locks")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	inst := session.NewInstance("delete-discard-failure", t.TempDir())
	inst.Status = session.StatusStopped
	msg := (&Home{}).deleteSession(inst)()
	if _, ok := msg.(sessionDeleteFailedMsg); !ok {
		t.Fatalf("deleteSession() = %T, want sessionDeleteFailedMsg", msg)
	}
}

func TestRemoveSessionDeletesStoppedTmuxSession(t *testing.T) {
	inst := session.NewInstance("stopped-removal-guard", t.TempDir())
	inst.Status = session.StatusStopped

	msg := (&Home{}).removeSession(inst)()
	deleted, ok := msg.(sessionDeletedMsg)
	if !ok || deleted.deletedID != inst.ID {
		t.Fatalf("removeSession() = %T %#v, want sessionDeletedMsg for %q", msg, msg, inst.ID)
	}
}
