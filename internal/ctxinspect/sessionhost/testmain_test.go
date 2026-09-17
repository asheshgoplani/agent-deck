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
	cleanupHome := testutil.IsolateHome()
	home := os.Getenv("HOME")
	for k, v := range map[string]string{
		"CLAUDE_CONFIG_DIR": home + "/.claude",
		"CODEX_HOME":        home + "/.codex",
	} {
		if err := os.Setenv(k, v); err != nil {
			panic("sessionhost: cannot isolate " + k + ": " + err.Error())
		}
	}
	cleanupTmux := testutil.IsolateTmuxSocket()
	code := m.Run()
	cleanupTmux()
	cleanupHome()
	os.Exit(code)
}
