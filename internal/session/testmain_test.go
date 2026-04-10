package session

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// skipIfNoTmuxServer skips the test if tmux binary is missing or server isn't running.
// This prevents test failures in CI environments where tmux is installed but no server runs.
func skipIfNoTmuxServer(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	// Check if tmux server is running by trying to list sessions
	if err := exec.Command("tmux", "list-sessions").Run(); err != nil {
		t.Skip("tmux server not running")
	}
}

func TestMain(m *testing.M) {
	// Git hooks export GIT_DIR/GIT_WORK_TREE; clear them so test subprocess git
	// commands operate on their temp repos instead of the real repository.
	testutil.UnsetGitRepoEnv()

	restoreHome, err := testutil.SetupTestHome()
	if err != nil {
		panic(err)
	}
	defer restoreHome()
	ClearUserConfigCache()

	// Force test profile to prevent production data corruption
	// See CLAUDE.md: "2025-12-11 Incident: Tests with AGENTDECK_PROFILE=work overwrote ALL 36 production sessions"
	os.Setenv("AGENTDECK_PROFILE", "_test")

	// Run tests
	code := m.Run()

	// Cleanup: Kill any orphaned test sessions after tests complete
	// This prevents RAM waste from lingering test sessions
	// See CLAUDE.md: "2026-01-20 Incident: 20+ Test-Skip-Regen sessions orphaned, wasting ~3GB RAM"
	cleanupTestSessions()

	os.Exit(code)
}

// cleanupTestSessions kills any tmux sessions created during testing.
// IMPORTANT: Only match specific known test artifacts, NOT broad patterns.
// Broad patterns like HasPrefix("agentdeck_test") or Contains("test_") kill
// real user sessions with "test" in their title. Each test already has
// defer Kill() which handles cleanup reliably (runs on panic, Fatal, etc).
func cleanupTestSessions() {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return
	}

	sessions := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, sess := range sessions {
		if strings.Contains(sess, "Test-Skip-Regen") {
			_ = exec.Command("tmux", "kill-session", "-t", sess).Run()
		}
	}
}
