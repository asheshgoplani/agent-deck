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
	restoreHome, err := testutil.SetupTestHome()
	if err != nil {
		panic(err)
	}
	defer restoreHome()

	os.Setenv("AGENTDECK_PROFILE", "_test")

	code := m.Run()

	cleanupTestSessions()

	os.Exit(code)
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
