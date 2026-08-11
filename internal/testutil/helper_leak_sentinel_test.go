package testutil_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// helperLeakCases pin the packages whose tests re-exec the test binary as a
// helper subprocess. Each names a fast test that drives that re-exec, so the
// child's TestMain runs its real isolation path.
var helperLeakCases = []struct {
	pkg  string
	run  string
	what string
}{
	{"./cmd/agent-deck/", "^TestCursorHooks", "cursor-hooks exit-path helpers"},
	{"./internal/ui/", "^TestOrphanWatchdog_ForceExitsHungProcess$", "orphan-watchdog child"},
}

// TestHelperSubprocessesLeaveNoTempDirs is the behavioral guard for the second
// half of the 2026-08-10 temp-leak incident.
//
// Tests that exercise a production exit path re-exec the test binary
// (`exec.Command(os.Args[0], "-test.run=TestSomeHelperProcess")`) and let the
// child die by os.Exit. os.Exit runs no deferred functions, so every temp dir
// the CHILD's TestMain created is stranded — one temp HOME and one ad-tmux-*
// socket dir per invocation, forever. 644 ad-tmux-* dirs had accumulated under
// /tmp when this was found, 592 of them empty.
//
// A unit test cannot catch this: the parent's own cleanup runs fine and the
// child is a separate process. So this spawns the REAL package suite with a
// private TMPDIR and asserts the directory is empty of owned prefixes after a
// successful run. -count=1 forces the child to actually run; a cached result
// would execute no TestMain and leak nothing, passing falsely.
//
// Note ad-tmux-*/ad-sock-* dirs are created under /tmp (short sun_path), not
// TMPDIR, so this asserts on the temp HOME prefixes plus any owned dir that
// does land in TMPDIR. testutil.TmuxAlreadyIsolated's own doc covers the socket
// half, and both halves share one guard in each TestMain.
func TestHelperSubprocessesLeaveNoTempDirs(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns child `go test`; skipped in -short")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	if testutil.RaceEnabled {
		t.Skip("spawns nested `go test` subprocesses; skipped under -race to avoid the resource contention that destabilizes the full -race suite. TestNoUncleanedMkdirTempInTests and TestPackageTestRunLeavesNoTempDirs cover the static and single-package cases on every run.")
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed — cannot locate repo root")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))

	for _, tc := range helperLeakCases {
		tc := tc
		t.Run(tc.pkg, func(t *testing.T) {
			tmpdir := t.TempDir()

			cmd := exec.Command("go", "test", "-count=1", "-run", tc.run, tc.pkg)
			cmd.Dir = repoRoot
			cmd.Env = append(envWithout(os.Environ(), "TMPDIR"), "TMPDIR="+tmpdir)

			out, err := cmd.CombinedOutput()
			if err != nil {
				// FAIL, not Skip. A guard that skips itself whenever the thing it
				// guards misbehaves reports success on exactly the runs that
				// deserve attention, and a genuine regression that also breaks the
				// child suite would disappear silently. Environmental
				// unsupportedness is already handled up front (no `go` on PATH,
				// -short, -race); anything reaching here is the child suite really
				// failing, which is a finding.
				t.Fatalf("%s failed to run to completion (%v), so its temp dirs cannot "+
					"be assessed. Fix the child suite, or narrow -run %q if the selected "+
					"tests no longer exist.\n\nchild output:\n%s",
					tc.pkg, err, tc.run, out)
			}

			if leaked := leakedTempEntries(t, tmpdir); len(leaked) > 0 {
				t.Fatalf("%s stranded %d temp dir(s) via its %s:\n  - %s\n\n"+
					"A re-exec'd helper inherits an already-sandboxed environment and then "+
					"os.Exits past every defer, so it must NOT isolate again. Guard the "+
					"TestMain with testutil.HomeAlreadyIsolated / testutil.TmuxAlreadyIsolated.",
					tc.pkg, len(leaked), tc.what, strings.Join(leaked, "\n  - "))
			}
		})
	}
}
