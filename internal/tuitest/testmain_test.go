package tuitest

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestMain ensures all tuitest smoke tests use the _test profile to prevent
// accidental modification of production session data.
// See CLAUDE.md: "Never delete these TestMain files."
func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

// runTestMain holds the real TestMain body so the cleanup defers below actually
// run: TestMain calls os.Exit, which does NOT run deferred functions, so
// registering them here and returning the exit code is the only way to guarantee
// the isolated TMUX_TMPDIR and HOME temp dirs are removed (2026-06-07
// pty-exhaustion incident class).
func runTestMain(m *testing.M) int {
	// Isolate HOME+XDG so agent-deck path resolution lands in a temp dir, never
	// the real ~/.agent-deck (2026-06-04 data-loss incident, S5).
	// See internal/testutil/homeenv.go for the postmortem.
	cleanupHome := testutil.IsolateHome()
	defer cleanupHome()

	// Isolate the tmux socket. Smoke tests drive real tmux sessions; without
	// isolation they hit the user's default socket and destabilize live
	// agent-deck sessions (2026-04-17 incident).
	// See internal/testutil/tmuxenv.go for the full postmortem.
	cleanupTmux := testutil.IsolateTmuxSocket()
	defer cleanupTmux()

	os.Setenv("AGENTDECK_PROFILE", "_test")

	code := m.Run()

	cleanupTestSessions()

	return code
}

// cleanupTestSessions kills tmux sessions created by smoke tests.
// Only matches the specific "tuitest_" prefix used by this package.
func cleanupTestSessions() {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return
	}

	sessions := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, sess := range sessions {
		if strings.HasPrefix(sess, "tuitest_") {
			_ = exec.Command("tmux", "kill-session", "-t", sess).Run()
		}
	}
}
