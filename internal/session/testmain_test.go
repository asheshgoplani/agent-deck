package session

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// bootstrapSessionName is the idle tmux session kept alive for the lifetime
// of this test binary so skipIfNoTmuxBinary never silently no-ops tests that
// expect `tmux list-sessions` to succeed. See .planning/v1716-cleanup/PLAN.md
// concern 3 and .planning/verify-today-sprint/REPORT.md F3.
const bootstrapSessionName = "agent-deck-test-bootstrap"

// skipIfNoTmuxBinary skips the test only when the tmux binary is absent from
// PATH. Historically this was skipIfNoTmuxServer which ALSO skipped when
// `tmux list-sessions` failed -- that silently dropped regression coverage
// in isolated TMUX_TMPDIR environments (fresh socket, no server).
// TestMain now bootstraps a server in the isolated socket, so the server
// check is no longer necessary.
func skipIfNoTmuxBinary(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
}

// skipIfNoTmuxServer preserves the pre-bootstrap skip semantics for every
// legacy test that depends on a "real" tmux server (one outside of our
// TestMain bootstrap). Without this, adding the bootstrap would force
// latent-broken tests that silently-skipped for months to actively run
// and fail in CI. Specifically: tests that call inst.Start() with
// Tool="claude" need a live claude pane, which CI cannot provide.
//
// Semantics:
//
//   - skip if tmux binary is missing;
//   - skip if `tmux list-sessions` fails (cold socket);
//   - skip if the ONLY session present is the bootstrap.
//
// New tests that want to leverage the bootstrap should call the more
// precise skipIfNoTmuxBinary helper directly. The #610 CLI-parity and
// #618 OSC tests are migrated to skipIfNoTmuxBinary so they actively run.
func skipIfNoTmuxServer(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		t.Skip("tmux server not running")
	}
	// Check for any non-bootstrap session.
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

// skipIfClaudePaneUnreliable skips when a claude pane launched via
// inst.Start() cannot be kept alive long enough to run tmux
// set-environment / get-environment. inst.Start() builds a command like
// `claude --session-id UUID --dangerously-skip-permissions` which exits
// immediately in most test environments (no such session id on disk).
//
// Before v1.7.16 the tests that call inst.Start() + SetEnvironment
// skipped implicitly because skipIfNoTmuxServer + isolated TMUX_TMPDIR
// returned no sessions. Now that TestMain bootstraps a server, the
// implicit gate is gone. Making the skip explicit keeps the pre-existing
// behaviour without pretending these tests run. Fixing the tests to use
// a manually-constructed long-lived tmux session (rather than
// inst.Start()) is tracked as a follow-up — out of scope for v1.7.16.
func skipIfClaudePaneUnreliable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude binary not available (test requires a live claude pane)")
	}
	// Probe by spawning an Instance exactly the way each test does, then
	// checking if the pane stays alive for a short settle. If it dies, the
	// real test can't run either. This exactly mirrors buildClaudeCommand
	// and prepareCommand shape — the probe result matches reality.
	probe := NewInstanceWithTool("agent-deck-probe-inst", t.TempDir(), "claude")
	if err := probe.Start(); err != nil {
		t.Skipf("probe Start failed in this env: %v", err)
	}
	// Poll for pane death over a generous window. Claude's "fast exit"
	// timing varies with system load and whether earlier tests left
	// state in ~/.claude; a single 400ms sample was flaky under the
	// full test suite (2/5 false-positives observed).
	deadline := time.Now().Add(2 * time.Second)
	alive := true
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		ps := probe.GetTmuxSession()
		if ps == nil || !ps.Exists() || ps.IsPaneDead() {
			alive = false
			break
		}
	}
	_ = probe.Kill()
	if !alive {
		t.Skip("claude pane died shortly after Start() in this environment (no claude auth/config, fast-exit on flags, etc.)")
	}
}

// skipIfNoClaudeBinary is the alias name older call sites use. Kept so
// the 5 platform-test sites compile; semantics are now
// skipIfClaudePaneUnreliable (probe for stay-alive, not just PATH
// presence).
func skipIfNoClaudeBinary(t *testing.T) {
	t.Helper()
	skipIfClaudePaneUnreliable(t)
}

func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

// runTestMain holds the real TestMain body. It exists so the cleanup defers
// below actually run: TestMain calls os.Exit, which does NOT run deferred
// functions, so registering them here and returning the exit code is the only
// way to guarantee the bootstrap tmux server is killed and the isolated
// TMUX_TMPDIR is removed. Skipping this leaked a tmux server (1 pty) on every
// run — the 2026-06-07 pty-exhaustion incident.
func runTestMain(m *testing.M) int {
	// Isolate HOME+XDG FIRST so every path this package resolves (config.json,
	// profiles/<p>/state.db, worker-scratch, logs) lands in a temp dir, never
	// the real ~/.agent-deck (2026-06-04 data-loss incident, S5).
	// See internal/testutil/homeenv.go for the postmortem.
	cleanupHome := testutil.IsolateHome()
	defer cleanupHome()

	// Git hooks export GIT_DIR/GIT_WORK_TREE; clear them so test subprocess git
	// commands operate on their temp repos instead of the real repository.
	testutil.UnsetGitRepoEnv()
	isolatePackageHome("agent-deck-session-tests-home-*")

	// Isolate the tmux socket. Without this, tests spawn tmux sessions on the
	// user's default socket and destabilize live agent-deck sessions.
	// 2026-04-17 incident: go test ./... killed every session in the personal
	// profile when a maintainer ran tests during PR review.
	// See internal/testutil/tmuxenv.go for the full postmortem.
	cleanupTmux := testutil.IsolateTmuxSocket()
	defer cleanupTmux()

	// Bootstrap an idle detached tmux session in the isolated socket so
	// `tmux list-sessions` succeeds for the lifetime of the test binary.
	// Without this, regression tests that depend on a running tmux server
	// (e.g. #610 CLI parity, #618 OSC cleanup) silently SKIP on cold-boot
	// local runs and pass only when an earlier test happened to start one.
	// See .planning/v1716-cleanup/PLAN.md concern 3.
	cleanupBootstrap := bootstrapTmuxServer()
	defer cleanupBootstrap()

	// Force test profile to prevent production data corruption
	// See CLAUDE.md: "2025-12-11 Incident: Tests with AGENTDECK_PROFILE=work overwrote ALL 36 production sessions"
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

// bootstrapTmuxServer starts a detached no-op tmux session in the currently
// isolated socket and returns a cleanup that kills the server at test-suite
// exit. If tmux is not installed, it's a no-op and tests simply skip via
// skipIfNoTmuxBinary. Any start error is printed (not fatal) so environments
// without tmux can still run non-tmux tests.
func bootstrapTmuxServer() func() {
	if _, err := exec.LookPath("tmux"); err != nil {
		return func() {}
	}
	cmd := exec.Command("tmux", "new-session", "-d", "-s", bootstrapSessionName, "sh", "-c", "sleep 3600")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "bootstrapTmuxServer: new-session failed: %v (%s)\n", err, strings.TrimSpace(string(out)))
		return func() {}
	}
	return func() {
		_ = exec.Command("tmux", "kill-server").Run()
	}
}

// TestTmuxBootstrap_ServerIsRunning pins that TestMain started a tmux server
// in the isolated socket so downstream tests no longer silent-skip on fresh
// TMUX_TMPDIR runs. Required regression guard for F3 of the sprint report.
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
