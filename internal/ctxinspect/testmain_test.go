package ctxinspect

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestMain isolates the package from the developer's real home directory.
//
// This is mandatory in this repository: several packages resolve agent-deck's
// live profile and config out of $HOME / $XDG_*, and a test run that reaches
// them has three times wiped a maintainer's live session index. This package
// performs no writes, but the isolation is set up unconditionally so that a
// later test added here cannot reintroduce the hazard.
func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

func runTestMain(m *testing.M) int {
	cleanupHome := testutil.IsolateHome()
	defer cleanupHome()
	dir, err := os.MkdirTemp("", "ctxinspect-home-")
	if err != nil {
		panic("ctxinspect: cannot create isolated HOME for tests: " + err.Error())
	}
	for k, v := range map[string]string{
		"HOME":              dir,
		"XDG_CONFIG_HOME":   dir + "/.config",
		"XDG_DATA_HOME":     dir + "/.local/share",
		"XDG_CACHE_HOME":    dir + "/.cache",
		"XDG_STATE_HOME":    dir + "/.local/state",
		"CLAUDE_CONFIG_DIR": dir + "/.claude",
		"AGENTDECK_PROFILE": "_test",
	} {
		if err := os.Setenv(k, v); err != nil {
			panic("ctxinspect: cannot isolate " + k + ": " + err.Error())
		}
	}
	cleanupTmux := testutil.IsolateTmuxSocket()
	defer cleanupTmux()
	code := m.Run()
	_ = os.RemoveAll(dir)
	return code
}
