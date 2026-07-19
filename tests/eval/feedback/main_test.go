//go:build eval_smoke

package feedback_test

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

// TestMain releases the shared agent-deck binary build dir after the package's
// tests finish. buildAgentDeck builds it once (and it must outlive every
// individual test), so t.Cleanup can't own it — the temp dir would otherwise
// leak on every `go test` run.
func TestMain(m *testing.M) {
	code := m.Run()
	harness.RemoveBuildArtifacts()
	os.Exit(code)
}
