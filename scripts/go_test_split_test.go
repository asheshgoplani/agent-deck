package scripts

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// scripts/go-test-split.sh cuts one go test command into a -run half and a
// -skip half with the same regex. tests/ci/go-test-split.test.sh checks that
// with a stub runner (no go test runs); this runs it with the Go suite so a
// change that breaks the partition fails CI.
func TestGoTestSplitScript(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	out, err := exec.Command("bash", filepath.Join("..", "tests", "ci", "go-test-split.test.sh")).CombinedOutput()
	if err != nil {
		t.Fatalf("go-test-split.test.sh: %v\n%s", err, out)
	}
}
