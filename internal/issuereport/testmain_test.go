package issuereport

import (
	"os"
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

	// Isolate the tmux socket. Without this, tests touching session lifecycle
	// paths spawn tmux on the user's default socket and destabilize live
	// agent-deck sessions (2026-04-17 incident).
	// See internal/testutil/tmuxenv.go for the full postmortem.
	cleanupTmux := testutil.IsolateTmuxSocket()
	defer cleanupTmux()

	// Force test profile to prevent production data corruption.
	// See CLAUDE.md: "2025-12-11 Incident: Tests with AGENTDECK_PROFILE=work overwrote ALL 36 production sessions"
	os.Setenv("AGENTDECK_PROFILE", "_test")

	// Pin the machine identity Scrub removes, so tests do not depend on
	// the host they run on.
	localIdentity = func() ([]string, []string) {
		return []string{"devbox7.corp.example.lan", "devbox7"}, []string{"alicew"}
	}
	// No real tmux / sw_vers probes from tests.
	commandOutput = func(string, ...string) string { return "" }
	// Tests run in containers without DISPLAY; pin "a browser can open"
	// so the Opener fakes are exercised. Headless tests override it.
	noBrowser = func() bool { return false }
	return m.Run()
}
