//go:build eval_smoke

package helpconsent_test

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

func TestMain(m *testing.M) {
	code := m.Run()
	harness.RemoveBuildArtifacts()
	os.Exit(code)
}
