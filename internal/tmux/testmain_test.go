package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// bootstrapSessionName is the idle tmux session kept alive for the lifetime
// of this test binary so skipIfNoTmuxBinary doesn't silently no-op regression
// tests. See .planning/v1716-cleanup/PLAN.md concern 3.
const bootstrapSessionName = "agent-deck-tmux-test-bootstrap"

// skipIfNoTmuxBinary skips the test only when the tmux binary is absent from
// PATH. Previously skipIfNoTmuxServer ALSO skipped on "server not running",
// which silently no-op'd #610/#618 regression tests inside isolated
// TMUX_TMPDIR. TestMain now bootstraps a server so the server check is no
// longer needed -- we skip only when tmux itself is missing.
func skipIfNoTmuxBinary(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
}

// skipIfNoTmuxServer preserves pre-bootstrap semantics for legacy tests
// (see the session package's testmain_test.go for full rationale). Skips
// when only the bootstrap session is present so tests that need a real
// external live session still silent-skip as before.
func skipIfNoTmuxServer(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		t.Skip("tmux server not running")
	}
	hasReal := false
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == bootstrapSessionName {
			continue
		}
		hasReal = true
		break
	}
	if !hasReal {
		t.Skip("tmux server has only the bootstrap session; legacy test requires a real live session")
	}
}

// TestMain ensures all tmux tests use the _test profile to prevent
// accidental modification of production data.
// CRITICAL: This was missing and caused test data to overwrite production sessions!
func TestMain(m *testing.M) {
	// Child-helper mode for softkill_test.go (#737). When SOFTKILL_TEST_HELPER
	// is set the test binary re-executes as a small SIGTERM-handling program
	// instead of running tests. Must come first — before any tmux setup — so
	// the helper child stays lightweight.
	if role := os.Getenv("SOFTKILL_TEST_HELPER"); role != "" {
		runSoftkillHelper(role)
		return
	}

	// Isolate the tmux socket. Without this, `tmux new-session` / `list-sessions` /
	// `kill-session` calls in test setup & cleanup hit the user's default
	// /tmp/tmux-<uid>/default socket — destabilizing their live sessions.
	// 2026-04-17 incident: go test ./... killed every session in the personal
	// profile when a maintainer ran tests during PR review.
	// See internal/testutil/tmuxenv.go for the full postmortem.
	cleanupTmux := testutil.IsolateTmuxSocket()
	defer cleanupTmux()

	// Bootstrap an idle tmux server in the isolated socket so the tests that
	// depend on `tmux list-sessions` succeeding (#618 cleanup-attach OSC,
	// etc.) actively run rather than silent-skipping on cold-boot.
	cleanupBootstrap := bootstrapTmuxServer()
	defer cleanupBootstrap()

	// Force _test profile for all tests in this package
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

// bootstrapTmuxServer starts a detached no-op tmux session in the isolated
// socket so `tmux list-sessions` succeeds for the lifetime of this test
// binary. If tmux is not installed this is a no-op (tests skip anyway).
func bootstrapTmuxServer() func() {
	if _, err := exec.LookPath("tmux"); err != nil {
		return func() {}
	}
	cmd := exec.Command("tmux", "new-session", "-d", "-s", bootstrapSessionName, "sh", "-c", "sleep 3600")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "bootstrapTmuxServer(tmux): new-session failed: %v (%s)\n", err, strings.TrimSpace(string(out)))
		return func() {}
	}
	return func() {
		_ = exec.Command("tmux", "kill-server").Run()
	}
}

// TestTmuxBootstrap_ServerIsRunning pins that TestMain's bootstrap ran and
// `tmux list-sessions` succeeds before any other test runs. Regression guard
// against F3 silent-skip trap.
func TestTmuxBootstrap_ServerIsRunning(t *testing.T) {
	skipIfNoTmuxBinary(t)
	if err := exec.Command("tmux", "list-sessions").Run(); err != nil {
		t.Fatalf("tmux list-sessions failed after bootstrap: %v", err)
	}
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		t.Fatalf("list-sessions -F: %v", err)
	}
	if !strings.Contains(string(out), bootstrapSessionName) {
		t.Fatalf("bootstrap session %q not present; got: %s", bootstrapSessionName, string(out))
	}
}
