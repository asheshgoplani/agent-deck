package sessionhost

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestMain isolates the package from the developer's real home directory.
//
// This package is the one place ctxinspect touches internal/session, which
// resolves agent-deck's live profile, config and state database out of $HOME
// and $XDG_*. A test run that reaches them has three times wiped a maintainer's
// live session index, so the isolation is unconditional and covers every
// variable that participates in path resolution.
func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

func runTestMain(m *testing.M) int {
	cleanupHome := testutil.IsolateHome()
	defer cleanupHome()
	dir, err := os.MkdirTemp("", "ctxinspect-sessionhost-home-")
	if err != nil {
		panic("sessionhost: cannot create isolated HOME for tests: " + err.Error())
	}
	for k, v := range map[string]string{
		"HOME":              dir,
		"XDG_CONFIG_HOME":   dir + "/.config",
		"XDG_DATA_HOME":     dir + "/.local/share",
		"XDG_CACHE_HOME":    dir + "/.cache",
		"XDG_STATE_HOME":    dir + "/.local/state",
		"CLAUDE_CONFIG_DIR": dir + "/.claude",
		"CODEX_HOME":        dir + "/.codex",
		"AGENTDECK_PROFILE": "_test",
	} {
		if err := os.Setenv(k, v); err != nil {
			panic("sessionhost: cannot isolate " + k + ": " + err.Error())
		}
	}
	cleanupTmux := testutil.IsolateTmuxSocket()
	defer cleanupTmux()
	code := m.Run()
	_ = os.RemoveAll(dir)
	return code
}
