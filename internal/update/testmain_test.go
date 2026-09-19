package update

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// homeBeforeIsolation is the HOME this test binary started with: the real
// one on a maintainer Mac or a CI runner. The guard test
// (TestLaunchdDefaults_NeverResolveUnderRealHome) refuses any default path
// that resolves under it.
var homeBeforeIsolation = os.Getenv("HOME")

// TestMain moves HOME and the XDG dirs to a temp dir before any test runs:
// RebootstrapOptions.fill defaults the pending marker to the cache dir and
// the agents dir to ~/Library/LaunchAgents, and an accidental host run
// deleted a real pending marker (2026-09-19 review of #2312; the 2026-06-04
// data-loss class). It also drops the launchd service label a test process
// started under a com.agentdeck.* daemon would inherit, so no test defers
// an agent instead of restarting it.
func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

func runTestMain(m *testing.M) int {
	cleanupHome := testutil.IsolateHome()
	defer cleanupHome()
	// Nothing here talks to tmux, but every TestMain isolates the socket
	// (internal/testutil/testmain_audit_test.go) so a future test cannot
	// reach the live server by accident.
	cleanupTmux := testutil.IsolateTmuxSocket()
	defer cleanupTmux()
	os.Unsetenv(launchdServiceEnv)
	return m.Run()
}
