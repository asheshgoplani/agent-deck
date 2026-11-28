package voicestream

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestMain isolates HOME (the producer lock and the bus live under it) and
// the tmux socket, like every package that writes agent-deck state.
func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

func runTestMain(m *testing.M) int {
	cleanupHome := testutil.IsolateHome()
	defer cleanupHome()
	cleanupTmux := testutil.IsolateTmuxSocket()
	defer cleanupTmux()
	os.Setenv("AGENTDECK_PROFILE", "_test")
	return m.Run()
}
