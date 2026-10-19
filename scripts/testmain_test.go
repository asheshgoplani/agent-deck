package scripts

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

// runTestMain holds the TestMain body so the cleanup defers run before
// os.Exit.
func runTestMain(m *testing.M) int {
	// Agent-deck path resolution in these tests lands in a temp HOME.
	cleanupHome := testutil.IsolateHome()
	defer cleanupHome()

	// Git hooks export GIT_DIR/GIT_WORK_TREE and friends; clear them so the
	// git commands these tests run (init, commit, tag) act on their temp
	// repos and never on the repository the suite runs from.
	testutil.UnsetGitRepoEnv()

	// Any tmux a script starts uses an isolated socket.
	cleanupTmux := testutil.IsolateTmuxSocket()
	defer cleanupTmux()

	return m.Run()
}
