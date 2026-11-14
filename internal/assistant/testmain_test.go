package assistant

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestMain isolates HOME and the tmux socket like every other package that
// could reach agent-deck state. These tests use a fake executor and never
// run the agent-deck binary, but the guard costs nothing.
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
