package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// goldenDir holds one committed .golden text file per step/width pair.
// Regenerate with `make visual-check-golden` (UPDATE_GOLDEN=1); every
// regeneration still needs a reviewer's PASS on the diff before it lands.
//
// Resolved from this source file's own path (runtime.Caller), not a
// relative "testdata/golden": the caller's working directory varies (`go
// run` from the repo root, `go test` from the package directory, or
// TestVisualCheckAgainstRealBinary's own t.Chdir into a scratch dir), and
// goldens must always mean tools/visualcheck/testdata/golden regardless.
var goldenDir = func() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "testdata", "golden")
}()

func goldenPath(step, width string) string {
	return filepath.Join(goldenDir, step+"_"+width+".golden")
}

// goldenStatus is PASS (matches or was just written), DIFF (differs from a
// committed golden), or MISSING (no golden on disk yet and UPDATE_GOLDEN
// wasn't set — always a DIFF, never a silent PASS).
type goldenStatus struct {
	status string // PASS, DIFF, MISSING
	want   string
}

func compareGolden(step, width, got string) (goldenStatus, error) {
	path := goldenPath(step, width)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(goldenDir, 0755); err != nil {
			return goldenStatus{}, err
		}
		if err := os.WriteFile(path, []byte(got), 0644); err != nil {
			return goldenStatus{}, err
		}
		return goldenStatus{status: "PASS", want: got}, nil
	}
	want, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return goldenStatus{status: "MISSING"}, nil
		}
		return goldenStatus{}, err
	}
	if strings.TrimRight(string(want), "\n") == strings.TrimRight(got, "\n") {
		return goldenStatus{status: "PASS", want: string(want)}, nil
	}
	return goldenStatus{status: "DIFF", want: string(want)}, nil
}

// clippedDialogBorder reports a frame whose rounded dialog boxes are not
// whole: every "╭" top border needs its "╰" bottom border on screen. A
// dialog that fills the terminal and ends with a newline scrolls its top
// border off, and regeneration would otherwise write that frame as PASS.
func clippedDialogBorder(frame string) string {
	top, bottom := strings.Count(frame, "╭"), strings.Count(frame, "╰")
	if top == bottom {
		return ""
	}
	return fmt.Sprintf("dialog border clipped: %d top corners (╭), %d bottom corners (╰)", top, bottom)
}
