package telemetry

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

func runTestMain(m *testing.M) int {
	// Never resolve paths under the developer's real HOME.
	cleanupTmux := testutil.IsolateTmuxSocket()
	defer cleanupTmux()
	cleanupHome := testutil.IsolateHome()
	defer cleanupHome()
	os.Setenv("AGENTDECK_PROFILE", "_test")
	return m.Run()
}
