package ui

import (
	"os/exec"
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

func TestRemoveSessionDeletesStoppedTmuxSession(t *testing.T) {
	inst := session.NewInstance("stopped-removal-guard", t.TempDir())
	inst.Status = session.StatusStopped

	msg := (&Home{}).removeSession(inst)()
	deleted, ok := msg.(sessionDeletedMsg)
	if !ok || deleted.deletedID != inst.ID {
		t.Fatalf("removeSession() = %T %#v, want sessionDeletedMsg for %q", msg, msg, inst.ID)
	}
}
