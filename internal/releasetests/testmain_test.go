package releasetests

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestMain holds only the os.Exit call so the cleanup defers in
// runTestMain run.
func TestMain(m *testing.M) { os.Exit(runTestMain(m)) }

// runTestMain runs the package in a temp HOME and on an isolated tmux
// socket, so a run through plain go test on a developer box never writes
// into the real ~/.local/share/agent-deck, even when the update path test
// regresses before its own leak check.
func runTestMain(m *testing.M) int {
	cleanupHome := testutil.IsolateHome()
	defer cleanupHome()
	cleanupTmux := testutil.IsolateTmuxSocket()
	defer cleanupTmux()
	return m.Run()
}
