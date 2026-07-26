package fleet

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestMain holds only the os.Exit call; setup and cleanups live in runTestMain
// because os.Exit does not run deferred functions. Mirrors
// internal/atomicfile/testmain_test.go.
//
// This package's tests drive every side effect through injected function
// fields and never spawn tmux or touch storage, but the isolation is
// mandatory anyway: it is what stops a future test in this package from
// silently reaching the real ~/.agent-deck (2026-06-04) or the default tmux
// server (2026-04-17), and internal/testutil's testmain audit enforces it.
func TestMain(m *testing.M) { os.Exit(runTestMain(m)) }

func runTestMain(m *testing.M) int {
	cleanupHome := testutil.IsolateHome()
	defer cleanupHome()
	cleanupTmux := testutil.IsolateTmuxSocket()
	defer cleanupTmux()
	return m.Run()
}
