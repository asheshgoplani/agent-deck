package session

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// A row stored with created_at 0 reads back as the Unix epoch, not the zero
// time. It is still an imported row with no real creation time, so the
// restart guard must treat it like a zero CreatedAt.
func TestEpochCreatedAtCopyRefusesRestart(t *testing.T) {
	testutil.RequireTmux(t)
	isolateUserHomeForShellRestart(t)
	t.Setenv("AGENTDECK_PROFILE", "personal")

	for name, restart := range map[string]func(*Instance) error{
		"restart":       (*Instance).Restart,
		"restart-fresh": (*Instance).RestartFresh,
	} {
		t.Run(name, func(t *testing.T) {
			sess := profileImportTmuxSession(t, "epoch-"+name, "work", "work-id")
			copy := &Instance{ID: "personal-copy", Title: "epoch copy", GroupPath: "recovered",
				Tool: "shell", ProjectPath: t.TempDir(), tmuxSession: sess, CreatedAt: time.Unix(0, 0)}

			if err := restart(copy); err == nil {
				t.Errorf("%s of the epoch-dated copy succeeded against the owner's tmux session", name)
			}
			alive, err := sess.ProbeExists()
			if err != nil || !alive {
				t.Fatalf("%s stopped the owner's tmux session: alive=%v, err=%v", name, alive, err)
			}
		})
	}
}
