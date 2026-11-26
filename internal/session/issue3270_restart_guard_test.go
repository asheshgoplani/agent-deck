package session

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// An old recovered copy must not restart another profile's live tmux
// session: Restart respawns or recreates the pane and restamps its
// environment, and RestartFresh kills the old session outright.
func TestOldForeignTmuxCopyRefusesRestart(t *testing.T) {
	testutil.RequireTmux(t)
	isolateUserHomeForShellRestart(t)
	t.Setenv("AGENTDECK_PROFILE", "personal")

	for name, restart := range map[string]func(*Instance) error{
		"restart":       (*Instance).Restart,
		"restart-fresh": (*Instance).RestartFresh,
	} {
		t.Run(name, func(t *testing.T) {
			sess := profileImportTmuxSession(t, "restart-"+name, "work", "work-id")
			copy := &Instance{ID: "personal-copy", Title: "old copy", GroupPath: "recovered",
				Tool: "shell", ProjectPath: t.TempDir(), tmuxSession: sess}

			if err := restart(copy); err == nil {
				t.Errorf("%s of the old copy succeeded against the owner's tmux session", name)
			}
			alive, err := sess.ProbeExists()
			if err != nil || !alive {
				t.Fatalf("%s of the old copy stopped the owner's tmux session: alive=%v, err=%v", name, alive, err)
			}
			owner, id, err := sess.ReadAgentDeckIdentity()
			if err != nil || owner != "work" || id != "work-id" {
				t.Errorf("%s of the old copy restamped the owner's session: profile=%q id=%q err=%v", name, owner, id, err)
			}
		})
	}
}
