package ctxtext

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
// formats strings and touches nothing, but the isolation is set up
// unconditionally so a later test added here cannot reintroduce the hazard.
func TestMain(m *testing.M) {
	cleanupHome := testutil.IsolateHome()
	cleanupTmux := testutil.IsolateTmuxSocket()
	code := m.Run()
	cleanupTmux()
	cleanupHome()
	os.Exit(code)
}
