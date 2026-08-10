package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
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
	//
	// The marker check generalises this beyond the one helper that was named
	// here. Every re-exec'd helper in this package calls os.Exit to exercise a
	// production exit path, which skips TestMain's defers — so any helper that
	// isolated would strand its temp dirs on every invocation. The cursor-hooks
	// helper did exactly that, unnoticed, because it set its own marker env and
	// this check only knew about Task6's (2026-08-10 temp-leak incident).
	// testutil.HomeAlreadyIsolated is true only when a parent already sandboxed
	// HOME, so recognising a helper by its inherited sandbox — rather than by
	// name — covers helpers added later without another leak first.
	isHelperProcess := os.Getenv("AGENT_DECK_TASK6_HELPER_PROCESS") != "" ||
		testutil.HomeAlreadyIsolated()
	isQueueHandler := os.Getenv("AGENT_DECK_QUEUE_HANDLER") == "1"

	if !isHelperProcess {
		// Isolate HOME+XDG so agent-deck path resolution lands in a temp dir,
		// never the real ~/.agent-deck (2026-06-04 data-loss incident, S5).
		// Must run before anything resolves a path. See internal/testutil/homeenv.go.
		//
		// ONE call, not IsolateHome() plus a local isolatePackageHome(): the
		// old pair created two temp HOMEs per run and only cleaned up the one
		// the package never used. Because this package builds its own CLI with
		// `go build`, the orphan held a full Go module+build cache — ~1.5 GB
		// stranded in $TMPDIR on every run (2026-08-10 temp-leak incident).
		cleanupHome := testutil.IsolatePackageHome("agent-deck-cmd-tests-home-*")
		defer cleanupHome()
	}

	// Git hooks export GIT_DIR/GIT_WORK_TREE; clear them so test subprocess git
	// commands operate on their temp repos instead of the real repository.
	testutil.UnsetGitRepoEnv()

	// Isolate the tmux socket. Without this, cmd-level tests spawn tmux
	// sessions on the user's default socket and destabilize live agent-deck
	// sessions. 2026-04-17 incident: go test ./... killed every session in
	// the personal profile when tests ran on a live host.
	// See internal/testutil/tmuxenv.go for the full postmortem.
	//
	// Skipped when the socket is ALREADY isolated, which is exactly the
	// re-exec'd-helper case above: the helper inherits the parent's private
	// TMUX_TMPDIR, then os.Exits past every defer, stranding a fresh ad-tmux-*
	// dir under /tmp per invocation. Gated on the marker rather than on
	// isHelperProcess so isolation is skipped only when provably redundant.
	cleanupTmux := func() {}
	if !isQueueHandler && !testutil.TmuxAlreadyIsolated() {
		cleanupTmux = testutil.IsolateTmuxSocket()
	}
	defer cleanupTmux()

	// Force _test profile for all tests in this package
	os.Setenv("AGENTDECK_PROFILE", "_test")

	// Run tests
	code := m.Run()

	// Cleanup: Kill any orphaned test sessions after tests complete
	// This prevents RAM waste from lingering test sessions
	// See CLAUDE.md: "2026-01-20 Incident: 20+ Test-Skip-Regen sessions orphaned, wasting ~3GB RAM"
	cleanupTestSessions()

	// Release the shared CLI build dir. channelsCLIBinary builds it once and it
	// must outlive every individual test, so t.Cleanup cannot own it — it
	// leaked an agent-deck-channels-bin-* dir on every run before this call.
	removeChannelsCLIBinDir()

	// Same contract for the eval harness's shared binary: coldstart_perf_test.go
	// builds via harness.NewSandbox, whose buildAgentDeck caches a 45 MB
	// agent-deck-eval-bin-* dir for the lifetime of the test binary. Only the
	// two tests/eval packages used to release it, so every run of THIS package
	// stranded one (2026-08-10 temp-leak incident).
	harness.RemoveBuildArtifacts()

	return code
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
