package ask

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

// runTestMain holds the real TestMain body so the cleanup defers run (os.Exit
// skips deferred calls).
func runTestMain(m *testing.M) int {
	// Isolate HOME+XDG so every profile and ask dir lands in a temp dir,
	// never the real ~/.agent-deck (see internal/testutil/homeenv.go).
	cleanupHome := testutil.IsolateHome()
	defer cleanupHome()
	cleanupTmux := testutil.IsolateTmuxSocket()
	defer cleanupTmux()
	os.Unsetenv("AGENTDECK_INSTANCE_ID")
	return m.Run()
}
