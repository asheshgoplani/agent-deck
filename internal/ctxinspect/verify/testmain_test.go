package verify

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestMain isolates the package from the developer's real home directory.
//
// Nothing in this package reads $HOME, but the isolation is unconditional: this
// repository has three times had a test run resolve agent-deck's live profile
// out of the real home and destroy it, and "this package does not do that yet"
// is not a property a later test preserves.
func TestMain(m *testing.M) {
	cleanupHome := testutil.IsolateHome()
	cleanupTmux := testutil.IsolateTmuxSocket()
	code := m.Run()
	cleanupTmux()
	cleanupHome()
	os.Exit(code)
}
