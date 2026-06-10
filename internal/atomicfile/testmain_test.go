package atomicfile_test

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestMain holds only the os.Exit call. The setup+defers live in runTestMain so
// the cleanups actually run: os.Exit does NOT run deferred functions, so
// registering them here and returning the exit code is the only way to guarantee
// the isolated TMUX_TMPDIR is removed and HOME is restored. The previous
// `os.Exit(m.Run())` form leaked the bootstrap tmux server + temp dirs on every
// run (the 2026-06-07 pty-exhaustion incident; #1310 leak-audit). See
// internal/tmux/testmain_test.go for the reference pattern.
func TestMain(m *testing.M) { os.Exit(runTestMain(m)) }

func runTestMain(m *testing.M) int {
	cleanupHome := testutil.IsolateHome()
	defer cleanupHome()
	cleanupTmux := testutil.IsolateTmuxSocket()
	defer cleanupTmux()
	return m.Run()
}
