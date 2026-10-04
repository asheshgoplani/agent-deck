package comms_test

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestMain holds only the os.Exit call so the HOME restore in runTestMain
// runs. Dir resolves the real data path, which agentpaths refuses under test
// unless HOME is isolated (CI runs with the runner's real HOME).
func TestMain(m *testing.M) { os.Exit(runTestMain(m)) }

func runTestMain(m *testing.M) int {
	cleanupHome := testutil.IsolateHome()
	defer cleanupHome()
	cleanupTmux := testutil.IsolateTmuxSocket()
	defer cleanupTmux()
	return m.Run()
}
