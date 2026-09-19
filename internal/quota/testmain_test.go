package quota

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

// runTestMain holds the real body so the cleanup defer runs: TestMain calls
// os.Exit, which does not run deferred functions.
func runTestMain(m *testing.M) int {
	// Isolate HOME+XDG so agent-deck path resolution lands in a temp dir, never
	// the real ~/.cache/agent-deck (2026-06-04 data-loss incident).
	// See internal/testutil/homeenv.go.
	cleanupHome := testutil.IsolateHome()
	defer cleanupHome()

	// This package never shells out to tmux, but the repo-wide audit in
	// internal/testutil/testmain_audit_test.go requires every TestMain to
	// isolate the socket anyway: the guarantee is only worth anything if it
	// holds unconditionally, and a package that grows a tmux call later must
	// not have to remember. 2026-04-17 incident, see
	// internal/testutil/tmuxenv.go.
	cleanupTmux := testutil.IsolateTmuxSocket()
	defer cleanupTmux()

	return m.Run()
}
