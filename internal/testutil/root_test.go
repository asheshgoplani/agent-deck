package testutil

import (
	"os"
	"runtime"
	"testing"
)

// TestSkipIfRoot runs the helper in a subtest and checks it skips exactly
// when the process is root on a platform with Unix permissions.
func TestSkipIfRoot(t *testing.T) {
	ran := false
	t.Run("guarded", func(t *testing.T) {
		SkipIfRoot(t, "root bypasses file permission checks")
		ran = true
	})
	wantSkip := runtime.GOOS != "windows" && os.Geteuid() == 0
	if ran == wantSkip {
		t.Fatalf("SkipIfRoot: body ran=%v with euid=%d on %s, want skip=%v", ran, os.Geteuid(), runtime.GOOS, wantSkip)
	}
}
