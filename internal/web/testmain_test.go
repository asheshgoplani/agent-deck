package web

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestMain forces AGENTDECK_PROFILE=_test for all internal/web tests.
// This prevents integration tests that create real tmux sessions from
// running under the active production profile and corrupting session data.
// CRITICAL: Do not remove — see CLAUDE.md test isolation rules.
func TestMain(m *testing.M) {
	// Isolate HOME+XDG so agent-deck path resolution lands in a temp dir, never
	// the real ~/.agent-deck (2026-06-04 data-loss incident, S5).
	// See internal/testutil/homeenv.go for the postmortem.
	cleanupHome := testutil.IsolateHome()
	defer cleanupHome()

	// Isolate the tmux socket. Web integration tests create real tmux sessions;
	// without isolation those hit the user's default socket and destabilize
	// live agent-deck sessions (2026-04-17 incident).
	// See internal/testutil/tmuxenv.go for the full postmortem.
	cleanupTmux := testutil.IsolateTmuxSocket()
	defer cleanupTmux()

	os.Setenv("AGENTDECK_PROFILE", "_test")
	os.Exit(m.Run())
}
