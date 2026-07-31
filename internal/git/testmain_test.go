package git

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
	// Isolate HOME+XDG so any agent-deck path resolution lands in a temp dir,
	// never the real ~/.agent-deck (2026-06-04 data-loss incident, S5).
	// See internal/testutil/homeenv.go for the postmortem.
	cleanupHome := testutil.IsolateHome()
	defer cleanupHome()

	// Git hooks export GIT_DIR/GIT_WORK_TREE; clear them so test subprocess git
	// commands operate on their temp repos instead of the real repository.
	testutil.UnsetGitRepoEnv()

	// Isolate the tmux socket. Even this package's tests run under `go test ./...`,
	// which means other packages' tmux-spawning code runs in the same shell
	// invocation — we want every package's TestMain to enforce isolation so no
	// ordering surprise can leak onto the user's default socket (2026-04-17 incident).
	// See internal/testutil/tmuxenv.go for the full postmortem.
	cleanupTmux := testutil.IsolateTmuxSocket()
	defer cleanupTmux()

	// Every pre-existing test in this package that writes a worktree
	// setup/destruction script and drives it through a gated entry point
	// (CreateWorktreeWithSetup*, RunWorktreeSetupAfterCreate, RemoveWorktree)
	// predates the consent gate (scriptconsent.go) and asserts the script
	// just runs — go test has no TTY, so the gate's fail-closed "prompt"
	// default would otherwise block every one of them. "always" restores
	// that expectation package-wide; the gate's own behavior (prompt/never/
	// override) is covered independently and explicitly by
	// scriptconsent_test.go, which saves and restores this ambient value
	// around each of its subtests so it never leaks between tests.
	SetScriptConsentConfig(ScriptConsentConfig{Policy: ScriptConsentAlways})

	return m.Run()
}
