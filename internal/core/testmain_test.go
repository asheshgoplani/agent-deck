package core

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestMain isolates HOME/XDG and the tmux socket before anything resolves a
// path or spawns tmux (see internal/testutil/homeenv.go and tmuxenv.go).
func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

func runTestMain(m *testing.M) int {
	cleanupHome := testutil.IsolateHome()
	defer cleanupHome()
	testutil.UnsetGitRepoEnv()
	cleanupTmux := testutil.IsolateTmuxSocket()
	defer cleanupTmux()
	os.Setenv("AGENTDECK_PROFILE", "_test")
	return m.Run()
}
