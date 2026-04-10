package integration

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

func TestMain(m *testing.M) {
	// Git hooks export GIT_DIR/GIT_WORK_TREE; clear them so test subprocess git
	// commands operate on their temp repos instead of the real repository.
	testutil.UnsetGitRepoEnv()

	restoreHome, err := testutil.SetupTestHome()
	if err != nil {
		panic(err)
	}
	defer restoreHome()
	session.ClearUserConfigCache()

	// Force test profile to prevent production data corruption.
	os.Setenv("AGENTDECK_PROFILE", "_test")

	code := m.Run()

	// Cleanup: Kill any orphaned integration test sessions after tests complete.
	cleanupIntegrationSessions()

	os.Exit(code)
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
