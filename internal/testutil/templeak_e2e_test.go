package testutil_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// leakPrefixes are every temp-dir prefix the isolation helpers create. A normal
// test run must leave none of them behind.
var leakPrefixes = []string{
	"agent-deck-leakfixture-home-", // IsolatePackageHome, from the fixture
	"ad-home-",                     // IsolateHome
	"ad-tmux-",                     // IsolateTmuxSocket
	"ad-sock-",                     // ShortTmuxSocket
}

// TestPackageTestRunLeavesNoTempDirs is the end-to-end regression for the
// 2026-08-10 temp-leak incident.
//
// The unit tests around it prove each cleanup in isolation. This one proves the
// property that actually matters and that nothing else can: run a real `go test`
// on a package wired with the standard isolation helpers, under a TMPDIR nobody
// else touches, and assert that when it exits 0 the directory is clean.
//
// Before the fix this failed on two counts at once — cmd/agent-deck and
// internal/session each created a package HOME that nothing removed, and
// IsolateHome's `_ = os.RemoveAll(dir)` could not delete a HOME holding a
// read-only Go module cache and discarded the error saying so.
//
// The fixture deliberately plants that read-only module cache, so a regression
// in either half fails here.
func TestPackageTestRunLeavesNoTempDirs(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles and runs a fixture package in a subprocess; skipped under -short")
	}
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission semantics; the leak and its fix are Unix-only")
	}

	tmpdir := t.TempDir()
	fixturePkg := "./testdata/homeleakfixture"

	cmd := exec.Command(goToolPath(t), "test", "-count=1", fixturePkg)
	cmd.Dir = testutilPackageDir(t)
	// Only TMPDIR is redirected. Everything else — GOCACHE, GOMODCACHE, PATH —
	// is inherited, so the subprocess reuses the parent's build cache instead of
	// downloading a fresh module cache into the very directory under assertion.
	cmd.Env = append(envWithout(os.Environ(), "TMPDIR"), "TMPDIR="+tmpdir)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture package failed (%v); the leak assertion below is only "+
			"meaningful for a NORMAL, successful exit:\n%s", err, out)
	}

	leaked := leakedTempEntries(t, tmpdir)
	if len(leaked) > 0 {
		t.Fatalf("a successful `go test` run stranded %d temp entr(ies) in TMPDIR:\n  - %s\n\n"+
			"Every isolation helper must remove its temp dir on the normal exit path. "+
			"See internal/testutil/tempcleanup.go (RemoveTempTree) and homeenv.go "+
			"(IsolatePackageHome) for the 2026-08-10 postmortem.\n\nfixture output:\n%s",
			len(leaked), strings.Join(leaked, "\n  - "), out)
	}
}

func leakedTempEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read TMPDIR %s: %v", dir, err)
	}
	var leaked []string
	for _, e := range entries {
		for _, prefix := range leakPrefixes {
			if strings.HasPrefix(e.Name(), prefix) {
				leaked = append(leaked, e.Name())
				break
			}
		}
	}
	sort.Strings(leaked)
	return leaked
}

func envWithout(env []string, key string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// goToolPath resolves the go binary from PATH, which is how the go toolchain
// itself says to locate it (runtime.GOROOT is deprecated as of Go 1.24).
func goToolPath(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go toolchain not available: %v", err)
	}
	return path
}

func testutilPackageDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed — cannot locate the package directory")
	}
	return filepath.Dir(thisFile)
}
