package ui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestMain ensures all UI tests use the _test profile to prevent
// accidental modification of production data.
// CRITICAL: This was missing and caused test data to overwrite production sessions!
func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

// runTestMain holds the real TestMain body so the cleanup defers below actually
// run: TestMain calls os.Exit, which does NOT run deferred functions, so
// registering them here and returning the exit code is the only way to guarantee
// the isolated TMUX_TMPDIR and HOME temp dirs are removed (2026-06-07
// pty-exhaustion incident class).
func runTestMain(m *testing.M) int {
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

	// Hard backstop against orphaned/interrupted ui.test binaries pinning a CPU
	// core indefinitely (2026-06-21 incident: seven reparented ui.test processes
	// spun at ~100% CPU for >2 days and overheated the machine). See
	// armOrphanWatchdog for why -test.timeout alone does not save us.
	stopWatchdog := armOrphanWatchdog()
	defer stopWatchdog()

	// Force _test profile for all tests in this package
	os.Setenv("AGENTDECK_PROFILE", "_test")
	// Unit tests construct hundreds of Home models for synchronous behavior
	// checks. Their production workers are unrelated to those assertions and
	// must not outlive each test.
	homeBackgroundWorkersEnabled = false

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

// armOrphanWatchdog installs an independent, os.Exit-based deadline that
// guarantees this test binary terminates, and returns a func that disarms it.
//
// Why -test.timeout is not enough: before TestMain disabled Home's production
// workers, every model started statusWorker, the log-worker pool, and
// StorageWatcher.pollLoop, while most tests never tore them down. A timed-out
// run then tried to dump hundreds of thousands of goroutine stacks and could
// wedge before exiting. The worker gate prevents that leak; this watchdog stays
// as a backstop for explicit worker tests and any future lifecycle regression.
//
// os.Exit performs no stop-the-world and terminates immediately, so it is the
// only reliable backstop. The watchdog is armed for -test.timeout + grace, so
// it never fires on a healthy run (the soft timeout always gets first crack);
// it only matters once the normal dump has already wedged.
func armOrphanWatchdog() (disarm func()) {
	deadline := orphanHardDeadline()
	timer := time.AfterFunc(deadline, func() {
		fmt.Fprintf(os.Stderr,
			"\nFATAL: internal/ui orphan watchdog fired after %s (past -test.timeout).\n"+
				"A leaked background worker likely wedged the timeout's goroutine dump.\n"+
				"Forcing os.Exit(2) so this binary cannot spin on the CPU. "+
				"See internal/ui/testmain_test.go:armOrphanWatchdog.\n",
			deadline)
		os.Exit(2)
	})
	return func() { timer.Stop() }
}

// orphanHardDeadline derives the watchdog deadline from the active
// -test.timeout (read straight from os.Args, since it must work before the
// testing flags are parsed) plus a grace window. An env override keeps the
// regression test fast.
func orphanHardDeadline() time.Duration {
	const grace = 90 * time.Second
	const fallback = 10 * time.Minute // Go's own default -test.timeout

	if v := os.Getenv("AGENTDECK_TEST_HARD_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}

	switch soft := parseTestTimeoutArg(os.Args); {
	case soft > 0:
		return soft + grace
	default:
		// -test.timeout=0 disables the soft timeout entirely; keep a hard
		// ceiling anyway so a hung run still cannot spin forever.
		return fallback + grace
	}
}

// parseTestTimeoutArg extracts the -test.timeout duration from a compiled test
// binary's argv. Handles "-test.timeout=10m", "-test.timeout 10m", and the
// single-dash spellings `go test` may pass. Returns 0 when absent/unparseable.
func parseTestTimeoutArg(argv []string) time.Duration {
	for i, a := range argv {
		key := strings.TrimLeft(a, "-")
		if val, ok := strings.CutPrefix(key, "test.timeout="); ok {
			if d, err := time.ParseDuration(val); err == nil {
				return d
			}
			return 0
		}
		if key == "test.timeout" && i+1 < len(argv) {
			if d, err := time.ParseDuration(argv[i+1]); err == nil {
				return d
			}
			return 0
		}
	}
	return 0
}
