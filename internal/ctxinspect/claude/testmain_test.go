package claude

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestMain isolates the package from the developer's real home directory.
//
// This is mandatory in this repository: several packages resolve agent-deck's
// live profile and config out of $HOME / $XDG_*, and an un-sandboxed test run
// has three times wiped a maintainer's live session index. Nothing here writes
// outside t.TempDir, but the isolation is unconditional so a test added later
// cannot reintroduce the hazard — and so the memory walk, which reads $HOME
// while expanding "~", can never reach the real one.
func TestMain(m *testing.M) {
	cleanupHome := testutil.IsolateHome()
	if err := os.Setenv("CLAUDE_CONFIG_DIR", os.Getenv("HOME")+"/.claude"); err != nil {
		panic("claude: cannot isolate CLAUDE_CONFIG_DIR: " + err.Error())
	}
	cleanupTmux := testutil.IsolateTmuxSocket()
	code := m.Run()
	cleanupTmux()
	cleanupHome()
	os.Exit(code)
}

// emptyEnv is an environment probe that reports nothing set. Tests use it so a
// stray variable in the developer's shell cannot change a result.
func emptyEnv(string) string { return "" }

// envMap returns an environment probe backed by a map.
func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}
