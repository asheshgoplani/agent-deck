package integration

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

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

	// Git hooks export GIT_DIR/GIT_WORK_TREE; clear them so test subprocess git
	// commands operate on their temp repos instead of the real repository.
	testutil.UnsetGitRepoEnv()

	// Isolate the tmux socket. This package directly drives session lifecycle
	// code and was the package whose test run killed every user session during
	// the 2026-04-17 incident when go test ./... touched it without isolation.
	// See internal/testutil/tmuxenv.go for the full postmortem.
	cleanupTmux := testutil.IsolateTmuxSocket()
	defer cleanupTmux()

	// Force test profile to prevent production data corruption.
	os.Setenv("AGENTDECK_PROFILE", "_test")

	code := m.Run()

	// Cleanup: Kill any orphaned integration test sessions after tests complete.
	cleanupIntegrationSessions()

	return code
}

// cleanupIntegrationSessions kills tmux sessions with the integration test prefix.
// IMPORTANT: Only targets "agentdeck_inttest-" prefix, not broader patterns.
// Uses dashes because tmux sanitizeName converts underscores to dashes.
func cleanupIntegrationSessions() {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return
	}

	sessions := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, sess := range sessions {
		if strings.HasPrefix(sess, "agentdeck_inttest-") {
			_ = exec.Command("tmux", "kill-session", "-t", sess).Run()
		}
	}
}

// skipIfNoTmuxServer skips the test if tmux binary is missing or server isn't running.
// Centralized version replacing duplicated functions in session/ and tmux/ packages.
func skipIfNoTmuxServer(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	if err := exec.Command("tmux", "list-sessions").Run(); err != nil {
		t.Skip("tmux server not running")
	}
}

func TestIsolation_ProfileIsTest(t *testing.T) {
	profile := os.Getenv("AGENTDECK_PROFILE")
	if profile != "_test" {
		t.Fatalf("expected AGENTDECK_PROFILE=_test, got %q", profile)
	}
}
