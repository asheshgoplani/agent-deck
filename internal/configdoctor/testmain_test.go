package configdoctor

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestMain isolates HOME before any test runs. The doctor resolves Claude and
// Codex homes from config.toml and falls back to ~/.claude when a group has no
// override, so an unisolated run would read — and report on — the developer's
// real agent homes. os.Exit skips deferred functions, hence the runTestMain
// split (same reason as internal/atomicfile/testmain_test.go).
func TestMain(m *testing.M) { os.Exit(runTestMain(m)) }

func runTestMain(m *testing.M) int {
	cleanupHome := testutil.IsolateHome()
	defer cleanupHome()
	return m.Run()
}
