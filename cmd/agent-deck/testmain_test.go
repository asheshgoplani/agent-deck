package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestMain ensures all cmd tests use the _test profile to prevent
// accidental modification of production data.
// CRITICAL: This was missing and caused test data to overwrite production sessions!
func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

// runTestMain holds the real TestMain body. It exists so the cleanup defers
// below actually run: TestMain calls os.Exit, which does NOT run deferred
// functions, so registering them here and returning the exit code is the only
// way to guarantee the isolated TMUX_TMPDIR and HOME temp dirs are removed.
// Skipping this leaked per-run temp dirs on every run — the 2026-06-07
// pty-exhaustion incident class.
func runTestMain(m *testing.M) int {
	// Helper subprocesses (e.g. the Task6 XDG help-path test) are spawned by a
	// parent test that has ALREADY exported a safe, sandboxed HOME+XDG and set
	// the specific XDG_*_HOME values the subprocess must observe. Re-running
	// IsolateHome()/isolatePackageHome() here would clobber those inherited
	// values with a fresh ad-home-* temp dir, breaking the test (and silently
	// resolving to the wrong sandbox). The inherited env is already off the
	// real home, so data-safety is preserved by NOT re-isolating.
	isHelperProcess := os.Getenv("AGENT_DECK_TASK6_HELPER_PROCESS") != ""

	if !isHelperProcess {
		// Isolate HOME+XDG so agent-deck path resolution lands in a temp dir,
		// never the real ~/.agent-deck (2026-06-04 data-loss incident, S5).
		// Must run before anything resolves a path. See internal/testutil/homeenv.go.
		cleanupHome := testutil.IsolateHome()
		defer cleanupHome()
	}

	// Git hooks export GIT_DIR/GIT_WORK_TREE; clear them so test subprocess git
	// commands operate on their temp repos instead of the real repository.
	testutil.UnsetGitRepoEnv()
	if !isHelperProcess {
		isolatePackageHome("agent-deck-cmd-tests-home-*")
	}

	// Isolate the tmux socket. Without this, cmd-level tests spawn tmux
	// sessions on the user's default socket and destabilize live agent-deck
	// sessions. 2026-04-17 incident: go test ./... killed every session in
	// the personal profile when tests ran on a live host.
	// See internal/testutil/tmuxenv.go for the full postmortem.
	cleanupTmux := testutil.IsolateTmuxSocket()
	defer cleanupTmux()

	// Force _test profile for all tests in this package
	os.Setenv("AGENTDECK_PROFILE", "_test")

	// Run tests
	code := m.Run()

	// Cleanup: Kill any orphaned test sessions after tests complete
	// This prevents RAM waste from lingering test sessions
	// See CLAUDE.md: "2026-01-20 Incident: 20+ Test-Skip-Regen sessions orphaned, wasting ~3GB RAM"
	cleanupTestSessions()

	return code
}

func isolatePackageHome(pattern string) {
	home, err := os.MkdirTemp("", pattern)
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	// Clear (do NOT pin) XDG base dirs so they track HOME and don't accumulate
	// stale config/data across tests in this shared package home. See
	// testutil.IsolateHome's doc comment (2026-06-07 ~96-test isolation
	// regression from #1294's "prefer XDG if it exists" path resolution).
	os.Unsetenv("XDG_CONFIG_HOME")
	os.Unsetenv("XDG_DATA_HOME")
	os.Unsetenv("XDG_CACHE_HOME")
	os.Unsetenv("XDG_STATE_HOME")
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
