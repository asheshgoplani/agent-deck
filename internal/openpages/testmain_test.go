package openpages

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

func TestMain(m *testing.M) {
	cleanupHome := testutil.IsolateHome()
	cleanupTmux := testutil.IsolateTmuxSocket()
	code := m.Run()
	cleanupTmux()
	cleanupHome()
	os.Exit(code)
}
