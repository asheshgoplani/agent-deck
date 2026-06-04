package feedback_test

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

func TestMain(m *testing.M) {
	// Isolate HOME+XDG so agent-deck path resolution lands in a temp dir, never
	// the real ~/.agent-deck (2026-06-04 data-loss incident, S5).
	// See internal/testutil/homeenv.go for the postmortem.
	cleanupHome := testutil.IsolateHome()
	defer cleanupHome()

	// Isolate the tmux socket. Without this, tests touching session lifecycle
	// paths spawn tmux on the user's default socket and destabilize live
	// agent-deck sessions (2026-04-17 incident).
	// See internal/testutil/tmuxenv.go for the full postmortem.
	cleanupTmux := testutil.IsolateTmuxSocket()
	defer cleanupTmux()

	// Force test profile to prevent production data corruption.
	// See CLAUDE.md: "2025-12-11 Incident: Tests with AGENTDECK_PROFILE=work overwrote ALL 36 production sessions"
	os.Setenv("AGENTDECK_PROFILE", "_test")
	os.Exit(m.Run())
}
