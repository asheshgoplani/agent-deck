package testutil_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// plantReadOnlyModuleCache writes a tree shaped like the Go module cache the
// toolchain materialises under $HOME/go/pkg/mod: source files at 0444 inside
// directories at 0555. Unlinking a file requires write permission on its
// PARENT directory, so the 0555 dirs — not the 0444 files — are what make
// os.RemoveAll fail with EACCES.
//
// Returns the outermost planted directory so callers can force-restore
// permissions if an assertion fails before cleanup ran.
func plantReadOnlyModuleCache(t *testing.T, home string) string {
	t.Helper()

	modRoot := filepath.Join(home, "go", "pkg", "mod")
	pkgDir := filepath.Join(modRoot, "example.com", "leaky@v1.0.0", "internal")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir module cache: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "leaky.go"), []byte("package internal\n"), 0o444); err != nil {
		t.Fatalf("write module file: %v", err)
	}

	// Seal from the deepest directory outward, exactly as the go command does.
	for _, dir := range []string{
		pkgDir,
		filepath.Dir(pkgDir),
		filepath.Join(modRoot, "example.com"),
	} {
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatalf("chmod %s read-only: %v", dir, err)
		}
	}

	// Safety net: if the code under test fails to remove the tree, restore
	// write permission so this test never leaks the very thing it guards.
	t.Cleanup(func() { forceRemove(home) })

	return modRoot
}

func forceRemove(dir string) {
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
	_ = os.RemoveAll(dir)
}

// TestRemoveTempTreeRemovesReadOnlyModuleCache is the direct regression for the
// 2026-08-10 temp-leak incident: os.RemoveAll cannot delete a test HOME that a
// subprocess `go build` populated with a read-only module cache.
func TestRemoveTempTreeRemovesReadOnlyModuleCache(t *testing.T) {
	home := t.TempDir()
	plantReadOnlyModuleCache(t, home)

	// Establish the premise: plain os.RemoveAll fails on this tree. If Go or
	// the platform ever changes that, this guard is obsolete and should say so
	// rather than pass vacuously.
	if err := os.RemoveAll(home); err == nil {
		t.Fatal("premise broken: os.RemoveAll now removes a read-only module cache; " +
			"RemoveTempTree's chmod pass may no longer be needed")
	}

	if err := testutil.RemoveTempTree(home); err != nil {
		t.Fatalf("RemoveTempTree on read-only module cache: %v", err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("temp tree survived RemoveTempTree: stat err = %v", err)
	}
}

// TestRemoveTempTreeReportsFailure proves cleanup errors are returned, not
// swallowed. The pre-fix code wrote `_ = os.RemoveAll(dir)`, so 130 GB of
// leaked temp dirs accumulated without a single diagnostic.
func TestRemoveTempTreeReportsFailure(t *testing.T) {
	parent := t.TempDir()
	victim := filepath.Join(parent, "child")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatalf("mkdir victim: %v", err)
	}
	// Removing `victim` requires write permission on `parent`.
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatalf("chmod parent: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	if err := testutil.RemoveTempTree(victim); err == nil {
		t.Fatal("RemoveTempTree returned nil for an unremovable path; cleanup failures must surface")
	}
}

func TestRemoveTempTreeEmptyPathIsNoOp(t *testing.T) {
	if err := testutil.RemoveTempTree(""); err != nil {
		t.Fatalf("RemoveTempTree(\"\") = %v, want nil", err)
	}
}

// TestIsolateHomeCleanupRemovesTempDir covers the shared-helper half of the
// leak: IsolateHome's returned cleanup must actually delete its ad-home-* dir,
// even after a subprocess populated it with a read-only module cache.
func TestIsolateHomeCleanupRemovesTempDir(t *testing.T) {
	origHome := os.Getenv("HOME")

	cleanup := testutil.IsolateHome()
	home := os.Getenv("HOME")
	if home == "" || home == origHome {
		t.Fatalf("IsolateHome did not redirect HOME (got %q, orig %q)", home, origHome)
	}
	if filepath.Base(home) == "" || !isAdHomeDir(home) {
		t.Fatalf("IsolateHome HOME %q does not use the ad-home- prefix", home)
	}
	plantReadOnlyModuleCache(t, home)

	cleanup()

	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("IsolateHome cleanup left %s behind: stat err = %v", home, err)
	}
	if got := os.Getenv("HOME"); got != origHome {
		t.Fatalf("IsolateHome cleanup did not restore HOME: got %q, want %q", got, origHome)
	}
}

func isAdHomeDir(dir string) bool {
	base := filepath.Base(dir)
	return len(base) > len("ad-home-") && base[:len("ad-home-")] == "ad-home-"
}

// TestIsolatePackageHomeCleanupRemovesTempDir is the regression for the largest
// leak source: cmd/agent-deck and internal/session each created a package HOME
// with os.MkdirTemp and never removed it, so every `go test` run stranded a
// ~1.5 GB tree (Go module + build cache inside the redirected HOME).
func TestIsolatePackageHomeCleanupRemovesTempDir(t *testing.T) {
	origHome := os.Getenv("HOME")

	cleanup := testutil.IsolatePackageHome("agent-deck-unit-pkg-home-*")
	home := os.Getenv("HOME")
	if home == origHome {
		t.Fatal("IsolatePackageHome did not redirect HOME")
	}
	if base := filepath.Base(home); !hasPrefix(base, "agent-deck-unit-pkg-home-") {
		t.Fatalf("IsolatePackageHome ignored its prefix: HOME base = %q", base)
	}
	// The XDG bases must be cleared, not pinned, so they track the temp HOME.
	// See homeenv.go's postmortem on the 2026-06-07 isolation regression.
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		if v, ok := os.LookupEnv(key); ok {
			t.Fatalf("IsolatePackageHome left %s set to %q; it must be cleared", key, v)
		}
	}
	if os.Getenv(testutil.HomeIsolationMarkerEnv) != "1" {
		t.Fatalf("IsolatePackageHome did not set %s", testutil.HomeIsolationMarkerEnv)
	}
	plantReadOnlyModuleCache(t, home)

	cleanup()

	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("IsolatePackageHome cleanup left %s behind: stat err = %v", home, err)
	}
	if got := os.Getenv("HOME"); got != origHome {
		t.Fatalf("cleanup did not restore HOME: got %q, want %q", got, origHome)
	}
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// TestHomeAlreadyIsolatedRejectsUnbackedMarker covers the sharp edge of using an
// env var to DISABLE isolation: a marker left exported in a shell (or inherited
// from an unrelated tool) must not convince a TestMain that the real home is a
// sandbox. Getting this wrong re-arms the 2026-06-04 data-loss incident.
func TestHomeAlreadyIsolatedRejectsUnbackedMarker(t *testing.T) {
	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("cannot resolve the real home: %v", err)
	}

	t.Setenv(testutil.HomeIsolationMarkerEnv, "1")

	t.Setenv("HOME", realHome)
	if testutil.HomeAlreadyIsolated() {
		t.Error("marker set while HOME is the REAL home reported as isolated; a stale " +
			"exported marker would disable isolation and let tests write to ~/.agent-deck")
	}

	t.Setenv("HOME", "")
	if testutil.HomeAlreadyIsolated() {
		t.Error("empty HOME reported as isolated")
	}

	t.Setenv("HOME", t.TempDir())
	if !testutil.HomeAlreadyIsolated() {
		t.Error("marker set with HOME pointing at a temp dir must report isolated; " +
			"otherwise re-exec'd helpers isolate again and leak their temp dirs")
	}

	t.Setenv(testutil.HomeIsolationMarkerEnv, "")
	if testutil.HomeAlreadyIsolated() {
		t.Error("no marker must never report isolated")
	}
}

// TestReapStaleTempDirs covers the janitor that clears leftovers from runs the
// fixed cleanup path never got to run — a killed or crashed test binary. It
// must be narrow: only the exact owned prefix, only directories, and only ones
// older than the age guard.
func TestReapStaleTempDirs(t *testing.T) {
	base := t.TempDir()
	stale := time.Now().Add(-48 * time.Hour)

	mk := func(name string, age time.Time, isDir bool) string {
		p := filepath.Join(base, name)
		if isDir {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", name, err)
			}
		} else if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		if err := os.Chtimes(p, age, age); err != nil {
			t.Fatalf("chtimes %s: %v", name, err)
		}
		return p
	}

	oldOwned := mk("agent-deck-reap-test-111", stale, true)
	freshOwned := mk("agent-deck-reap-test-222", time.Now(), true)
	oldForeign := mk("some-other-tool-333", stale, true)
	oldOwnedFile := mk("agent-deck-reap-test-444", stale, false)
	// A prefix that merely SHARES a leading substring must not be swept.
	oldSimilar := mk("agent-deck-reap-testing-else-555", stale, true)

	n := testutil.ReapStaleTempDirs(base, "agent-deck-reap-test-*", 24*time.Hour)
	if n != 1 {
		t.Fatalf("ReapStaleTempDirs removed %d entries, want 1", n)
	}
	if _, err := os.Stat(oldOwned); !os.IsNotExist(err) {
		t.Errorf("stale owned dir survived: %v", err)
	}
	for _, keep := range []string{freshOwned, oldForeign, oldOwnedFile, oldSimilar} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("janitor removed %s, which it does not own: %v", keep, err)
		}
	}
}

// TestReapStaleTempDirsAcceptsTheHelperPrefixes pins the exact boundary the
// namespace rule sits on. "ad-home-" is the prefix IsolateHome itself uses and
// the single largest crash-leftover class, so a rule that required a prefix to
// be strictly LONGER than each owned entry — with "ad-home-" listed as owned —
// silently made the janitor a no-op for it. Every real caller pattern must be
// accepted here.
func TestReapStaleTempDirsAcceptsTheHelperPrefixes(t *testing.T) {
	patterns := []string{
		"ad-home-",
		"ad-tmux-",
		"ad-sock-",
		"agent-deck-cmd-tests-home-*",
		"agent-deck-session-tests-home-*",
		"agent-deck-channels-bin-*",
		"agent-deck-eval-bin-",
	}
	stale := time.Now().Add(-48 * time.Hour)

	for _, pattern := range patterns {
		base := t.TempDir()
		prefix := strings.TrimSuffix(pattern, "*")
		victim := filepath.Join(base, prefix+"123456")
		if err := os.MkdirAll(victim, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", victim, err)
		}
		if err := os.Chtimes(victim, stale, stale); err != nil {
			t.Fatalf("chtimes: %v", err)
		}

		if n := testutil.ReapStaleTempDirs(base, pattern, 24*time.Hour); n != 1 {
			t.Errorf("ReapStaleTempDirs(%q) removed %d entries, want 1 — the janitor "+
				"is a no-op for a prefix it is supposed to own", pattern, n)
		}
	}
}

// TestReapStaleTempDirsRefusesUnownedPrefix keeps the janitor from ever being
// pointed at a broad pattern. Anything outside agent-deck's own namespace — and
// any BARE namespace, which would sweep production dirs like
// agent-deck-mergeback-* — is a no-op, not a sweep.
func TestReapStaleTempDirsRefusesBareNamespace(t *testing.T) {
	stale := time.Now().Add(-48 * time.Hour)

	for _, pattern := range []string{"ad-", "agent-deck-", "agent-deck-*"} {
		base := t.TempDir()
		// A production temp dir that a bare-namespace sweep would destroy.
		victim := filepath.Join(base, "agent-deck-mergeback-987654")
		if err := os.MkdirAll(victim, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.Chtimes(victim, stale, stale); err != nil {
			t.Fatalf("chtimes: %v", err)
		}

		if n := testutil.ReapStaleTempDirs(base, pattern, 24*time.Hour); n != 0 {
			t.Errorf("ReapStaleTempDirs(%q) removed %d entries; a bare namespace must "+
				"never sweep — agent-deck-mergeback-* holds a live git worktree", pattern, n)
		}
		if _, err := os.Stat(victim); err != nil {
			t.Errorf("bare-namespace pattern %q destroyed a production temp dir: %v", pattern, err)
		}
	}
}

func TestReapStaleTempDirsRefusesUnownedPrefix(t *testing.T) {
	base := t.TempDir()
	for _, name := range []string{"tmp-1", "go-build-1", "x"} {
		if err := os.MkdirAll(filepath.Join(base, name), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		old := time.Now().Add(-72 * time.Hour)
		_ = os.Chtimes(filepath.Join(base, name), old, old)
	}

	for _, prefix := range []string{"", "*", "tmp-*", "go-build-", "x"} {
		if n := testutil.ReapStaleTempDirs(base, prefix, time.Hour); n != 0 {
			t.Fatalf("ReapStaleTempDirs(%q) removed %d entries; unowned prefixes must be refused", prefix, n)
		}
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("janitor deleted %d of 3 unowned dirs", 3-len(entries))
	}
}
