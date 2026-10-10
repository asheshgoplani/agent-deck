package testutil

import (
	"os"
	"runtime"
	"testing"
)

// SkipIfRoot skips the test when it runs as root on a Unix platform. Root
// bypasses file permission checks (CAP_DAC_OVERRIDE) and group ownership
// rules, so tests that assert a permission denial cannot pass there. why
// names the permission behavior root bypasses. CI runners are non-root, so
// these tests still run in CI.
func SkipIfRoot(t testing.TB, why string) {
	t.Helper()
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		t.Skipf("skipping as root: %s", why)
	}
}
