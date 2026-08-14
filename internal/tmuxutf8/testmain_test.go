package tmuxutf8_test

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestMain isolates HOME and the tmux socket for this package.
//
// The integration test in this package spawns a REAL tmux server (that is the
// only way to observe the #1867 downgrade — it happens inside tmux, not in Go),
// so the isolation is load-bearing, not ceremonial:
//
//   - testutil.IsolateHome keeps agent-deck path resolution off the real
//     ~/.agent-deck (2026-06-04 data-loss incident).
//   - testutil.IsolateTmuxSocket unsets TMUX/TMUX_PANE — a tmux client locates
//     its server from $TMUX FIRST, so an inherited $TMUX reaches the host's
//     live server no matter what TMUX_TMPDIR says — and repoints TMUX_TMPDIR at
//     a private dir (2026-04-17 / 2026-07-26 fleet-death incidents).
//
// On top of that every server this package starts is named with an explicit
// `-L`, and is torn down with a `-L`-scoped kill-server. There is no bare
// `tmux kill-server` anywhere in this package: an unqualified kill-server on
// the default socket ends every tmux session on the machine.
//
// The setup+defer body lives in runTestMain rather than TestMain because
// os.Exit does not run deferred functions — see
// internal/testutil.TestNoTestMainLeaksCleanupBehindOsExit.
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
