package multiclienttmux_test

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestMain isolates HOME+XDG and the tmux socket for this package so spawning
// real tmux servers from tests never touches the user's ~/.agent-deck or their
// live default tmux socket (2026-06-04 data-loss and 2026-04-17 session-kill
// incidents). Required by TestAllTestMainsIsolateTmuxSocket, which mandates
// both IsolateHome and IsolateTmuxSocket in every TestMain. os.Exit skips
// deferred calls, so cleanups run explicitly before it.
func TestMain(m *testing.M) {
	cleanupHome := testutil.IsolateHome()
	cleanupTmux := testutil.IsolateTmuxSocket()
	code := m.Run()
	cleanupTmux()
	cleanupHome()
	os.Exit(code)
}
