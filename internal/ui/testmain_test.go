package ui

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestMain ensures all UI tests use the _test profile to prevent
// accidental modification of production data.
// CRITICAL: This was missing and caused test data to overwrite production sessions!
func TestMain(m *testing.M) {
	// Isolate HOME+XDG FIRST. This package was the concrete trigger of the
	// 2026-06-04 data-loss incident (S5): it set AGENTDECK_PROFILE=_test but did
	// NOT override HOME/XDG, so an un-sandboxed `go test ./internal/ui/...`
	// resolved paths via the real $HOME and wiped the live profile index +
	// config. See internal/testutil/homeenv.go for the postmortem.
	cleanupHome := testutil.IsolateHome()
	defer cleanupHome()

	// Isolate the tmux socket. UI tests drive session-lifecycle flows end-to-end;
	// without isolation they spawn tmux on the user's default socket and
	// destabilize live agent-deck sessions (2026-04-17 incident).
	// See internal/testutil/tmuxenv.go for the full postmortem.
	cleanupTmux := testutil.IsolateTmuxSocket()
	defer cleanupTmux()

	// Force _test profile for all tests in this package
	os.Setenv("AGENTDECK_PROFILE", "_test")

	// v1.7.38: stub syncOptOutToConfig to a no-op by default so feedback
	// dialog tests that exercise stepRating 'n' / stepConfirm decline do
	// NOT write to the developer's real ~/.agent-deck/config.toml. Tests
	// that want to verify the sync actually runs install their own stub
	// via stubSyncOptOut(t).
	syncOptOutToConfig = func() {}

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
