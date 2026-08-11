// Package homeleakfixture is a miniature test package driven as a SUBPROCESS by
// TestPackageTestRunLeavesNoTempDirs in internal/testutil. It exists to prove
// the property end-to-end that unit tests can only assert piecewise: that a
// normal, successful `go test` run of a package using the standard isolation
// helpers leaves nothing behind in $TMPDIR.
//
// It lives under testdata/ so the go tool excludes it from ./... — it must run
// only when the driver runs it, with a controlled TMPDIR.
package homeleakfixture

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// FixtureHomePattern is the temp-HOME prefix this fixture isolates under. The
// driver asserts on the same literal; keep the two in sync.
const FixtureHomePattern = "agent-deck-leakfixture-home-*"

func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

func runTestMain(m *testing.M) int {
	cleanupHome := testutil.IsolatePackageHome(FixtureHomePattern)
	defer cleanupHome()

	cleanupTmux := testutil.IsolateTmuxSocket()
	defer cleanupTmux()

	return m.Run()
}

// TestFixturePopulatesReadOnlyModuleCache reproduces, cheaply, what a subprocess
// `go build` does to a redirected HOME: it fills <HOME>/go/pkg/mod with source
// files at 0444 inside directories at 0555. Those 0555 directories are what made
// plain os.RemoveAll fail — unlinking a file needs write permission on its
// parent — so the cleanup under test must survive them.
func TestFixturePopulatesReadOnlyModuleCache(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Fatal("HOME is empty; IsolatePackageHome did not run")
	}

	pkgDir := filepath.Join(home, "go", "pkg", "mod", "example.com", "leaky@v1.0.0")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir module cache: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "leaky.go"), []byte("package leaky\n"), 0o444); err != nil {
		t.Fatalf("write module file: %v", err)
	}
	for _, dir := range []string{pkgDir, filepath.Dir(pkgDir)} {
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatalf("chmod %s: %v", dir, err)
		}
	}
}
