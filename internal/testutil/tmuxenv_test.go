package testutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIsolateTmuxSocket verifies that the helper sets TMUX_TMPDIR to an
// isolated directory that is NOT the user's default /tmp/tmux-<uid>/ path,
// and that cleanup removes the directory.
func TestIsolateTmuxSocket(t *testing.T) {
	// Save and restore the env var around this test.
	orig, had := os.LookupEnv("TMUX_TMPDIR")
	defer func() {
		if had {
			os.Setenv("TMUX_TMPDIR", orig)
		} else {
			os.Unsetenv("TMUX_TMPDIR")
		}
	}()

	cleanup := IsolateTmuxSocket()

	// After call, TMUX_TMPDIR must be set.
	dir := os.Getenv("TMUX_TMPDIR")
	if dir == "" {
		t.Fatal("IsolateTmuxSocket did not set TMUX_TMPDIR")
	}

	// CRITICAL: must NOT be the default /tmp/tmux-<uid> location that the
	// user's real sessions use. That's the whole point of this helper.
	defaultDefault := "/tmp"
	if dir == defaultDefault {
		t.Errorf("IsolateTmuxSocket left TMUX_TMPDIR=%q (the default tmux dir) — this would NOT isolate from user sessions", dir)
	}
	if strings.HasPrefix(dir, "/tmp/tmux-") {
		t.Errorf("IsolateTmuxSocket set TMUX_TMPDIR=%q which is the user's real tmux dir pattern", dir)
	}

	// The directory should exist.
	if stat, err := os.Stat(dir); err != nil || !stat.IsDir() {
		t.Errorf("isolated temp dir %q does not exist or is not a directory: %v", dir, err)
	}

	// Cleanup removes the directory.
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("cleanup did not remove %q (stat err=%v)", dir, err)
	}
}

// TestIsolateTmuxSocket_UnsetsTmuxEnvVar is the regression test for the
// 2026-04-17 three-cascade incident.
//
// On hosts where agent-deck itself runs (i.e. every developer's laptop), every
// shell spawned by agent-deck lives inside a tmux pane. That pane sets the
// TMUX env var to the user's real socket, e.g. /tmp/tmux-1000/default,PID,N.
//
// tmux's client discovery order is: $TMUX → -S path → -L name → $TMUX_TMPDIR.
// If $TMUX is set, every later step is ignored — so setting TMUX_TMPDIR alone
// gives zero isolation when tests run inside a parent tmux session, which is
// the default on every developer host. The v1.7.3 fix missed this and tests
// silently ran against the user's real tmux server, killing all live sessions.
//
// IsolateTmuxSocket MUST unset TMUX before tests spawn any tmux process.
func TestIsolateTmuxSocket_UnsetsTmuxEnvVar(t *testing.T) {
	origTmux, hadTmux := os.LookupEnv("TMUX")
	origTmuxTmpdir, hadTmuxTmpdir := os.LookupEnv("TMUX_TMPDIR")
	defer func() {
		restore("TMUX", origTmux, hadTmux)
		restore("TMUX_TMPDIR", origTmuxTmpdir, hadTmuxTmpdir)
	}()

	// Simulate the real-world condition: test process spawned inside a parent
	// tmux pane, inheriting TMUX pointing at the user's socket.
	os.Setenv("TMUX", "/tmp/tmux-1000/default,12345,0")

	cleanup := IsolateTmuxSocket()
	defer cleanup()

	if got, ok := os.LookupEnv("TMUX"); ok && got != "" {
		t.Fatalf("IsolateTmuxSocket did not unset TMUX; still %q — this is the 2026-04-17 three-cascade bug. "+
			"TMUX takes precedence over TMUX_TMPDIR in tmux client discovery, so leaving it set means "+
			"TMUX_TMPDIR is ignored and test-spawned tmux sessions hit the user's default socket", got)
	}
}

// TestIsolateTmuxSocket_UnsetsTmuxPane ensures TMUX_PANE (the companion var
// set by tmux for panes) is also cleared. Some tmux internals read it when
// spawning child commands; leaving it set can confuse session discovery in
// edge cases.
func TestIsolateTmuxSocket_UnsetsTmuxPane(t *testing.T) {
	origPane, hadPane := os.LookupEnv("TMUX_PANE")
	origTmuxTmpdir, hadTmuxTmpdir := os.LookupEnv("TMUX_TMPDIR")
	defer func() {
		restore("TMUX_PANE", origPane, hadPane)
		restore("TMUX_TMPDIR", origTmuxTmpdir, hadTmuxTmpdir)
	}()

	os.Setenv("TMUX_PANE", "%42")

	cleanup := IsolateTmuxSocket()
	defer cleanup()

	if got, ok := os.LookupEnv("TMUX_PANE"); ok && got != "" {
		t.Fatalf("IsolateTmuxSocket did not unset TMUX_PANE; still %q", got)
	}
}

// TestIsolateTmuxSocket_CleanupKeepsIsolationSticky pins the deliberate
// INVERSION of this helper's original cleanup contract.
//
// Cleanup used to restore TMUX/TMUX_PANE, reasoning that `go test` should not
// "permanently break the developer's interactive shell env". It cannot: a child
// process has no way to alter its parent shell's environment. The only reader of
// the restored values was the dying test binary — and agent-deck leaves
// background goroutines running there (status pollers, Instance.watchForFastDeath)
// that outlive the test which started them. Restoring the values re-pointed
// those late tmux calls at the developer's REAL default server. It was caught in
// the act: a leaked watchForFastDeath goroutine running `tmux has-session`
// against /tmp/tmux-<uid>/default after the suite had printed PASS.
//
// So isolation is now sticky for the life of the process, and TMUX_TMPDIR is
// parked on a path that can never be a directory — a late tmux call fails
// instantly instead of reaching the live fleet, and cannot create a server that
// no teardown would ever reap.
func TestIsolateTmuxSocket_CleanupKeepsIsolationSticky(t *testing.T) {
	origTmux, hadTmux := os.LookupEnv("TMUX")
	origPane, hadPane := os.LookupEnv("TMUX_PANE")
	origTmuxTmpdir, hadTmuxTmpdir := os.LookupEnv("TMUX_TMPDIR")
	defer func() {
		restore("TMUX", origTmux, hadTmux)
		restore("TMUX_PANE", origPane, hadPane)
		restore("TMUX_TMPDIR", origTmuxTmpdir, hadTmuxTmpdir)
	}()

	// Simulate the outermost case: a test binary whose process has no isolation
	// yet, launched from inside the developer's tmux pane.
	os.Setenv("TMUX", "/tmp/tmux-1000/default,98765,0")
	os.Setenv("TMUX_PANE", "%99")
	os.Unsetenv("TMUX_TMPDIR")

	cleanup := IsolateTmuxSocket()

	// During isolation, both must be empty.
	if os.Getenv("TMUX") != "" || os.Getenv("TMUX_PANE") != "" {
		t.Fatal("during isolation, TMUX and TMUX_PANE must be unset")
	}

	dir := os.Getenv("TMUX_TMPDIR")
	cleanup()

	// After cleanup, the process must still be unable to reach a live server.
	if got := os.Getenv("TMUX"); got != "" {
		t.Errorf("cleanup restored TMUX=%q — a goroutine outliving the suite would "+
			"then resolve tmux to the developer's live server", got)
	}
	if got := os.Getenv("TMUX_PANE"); got != "" {
		t.Errorf("cleanup restored TMUX_PANE=%q; isolation must be sticky", got)
	}
	torn := os.Getenv("TMUX_TMPDIR")
	if torn == "" {
		t.Error("cleanup left TMUX_TMPDIR unset — an unselected tmux spawn then " +
			"resolves to the default socket /tmp/tmux-<uid>/default")
	}
	if torn == dir {
		t.Errorf("cleanup left TMUX_TMPDIR at the REMOVED isolated dir %q; a late "+
			"`new-session` would recreate it and leak a server nothing reaps", torn)
	}
	// The parked path must be unusable, not merely different: no component under
	// it can be created, so tmux fails before it can spawn anything.
	if err := os.MkdirAll(filepath.Join(torn, "probe"), 0o700); err == nil {
		t.Errorf("cleanup parked TMUX_TMPDIR at %q, but a directory could be created "+
			"there — a late tmux call could still start a server", torn)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("cleanup did not remove the isolated dir %q (stat err=%v)", dir, err)
	}
}

// TestIsolateTmuxSocket_CleanupRestoresAnOuterIsolatedDir covers the nested
// case: a test that isolates on top of its package's TestMain must hand the
// package's own isolation back, or every later test in that package would run
// against the parked (unusable) path. An outer dir that is already isolated is
// by definition safe to restore — which is exactly what distinguishes it from an
// unset or default-base value.
func TestIsolateTmuxSocket_CleanupRestoresAnOuterIsolatedDir(t *testing.T) {
	origTmuxTmpdir, hadTmuxTmpdir := os.LookupEnv("TMUX_TMPDIR")
	defer restore("TMUX_TMPDIR", origTmuxTmpdir, hadTmuxTmpdir)

	outer := t.TempDir()
	os.Setenv("TMUX_TMPDIR", outer)

	cleanup := IsolateTmuxSocket()
	inner := os.Getenv("TMUX_TMPDIR")
	if inner == outer {
		t.Fatalf("nested IsolateTmuxSocket did not repoint TMUX_TMPDIR (still %q)", outer)
	}
	cleanup()

	if got := os.Getenv("TMUX_TMPDIR"); got != outer {
		t.Errorf("cleanup left TMUX_TMPDIR=%q, want the outer isolated dir %q — later "+
			"tests in the package would lose their isolated socket", got, outer)
	}
}

// TestIsolateTmuxSocket_SetsTestIsolationMarker verifies the helper sets
// AGENT_DECK_TEST_ISOLATED=1, which downstream guards (e.g. a panic in
// internal/tmux.Start when this marker is present but TMUX still points at
// the user's socket) use as the test-context signal.
func TestIsolateTmuxSocket_SetsTestIsolationMarker(t *testing.T) {
	origMarker, hadMarker := os.LookupEnv("AGENT_DECK_TEST_ISOLATED")
	defer restore("AGENT_DECK_TEST_ISOLATED", origMarker, hadMarker)

	cleanup := IsolateTmuxSocket()
	defer cleanup()

	if got := os.Getenv("AGENT_DECK_TEST_ISOLATED"); got != "1" {
		t.Fatalf("IsolateTmuxSocket did not set AGENT_DECK_TEST_ISOLATED=1, got %q", got)
	}
}

// restore puts an env var back to its original state. Used by the regression
// tests above to avoid cross-test pollution.
func restore(key, orig string, had bool) {
	if had {
		os.Setenv(key, orig)
	} else {
		os.Unsetenv(key)
	}
}

// TestIsolateTmuxSocket_UniquePerCall ensures repeated calls return
// different directories — so parallel test binaries don't collide.
func TestIsolateTmuxSocket_UniquePerCall(t *testing.T) {
	// Save and restore
	orig, had := os.LookupEnv("TMUX_TMPDIR")
	defer func() {
		if had {
			os.Setenv("TMUX_TMPDIR", orig)
		} else {
			os.Unsetenv("TMUX_TMPDIR")
		}
	}()

	c1 := IsolateTmuxSocket()
	dir1 := os.Getenv("TMUX_TMPDIR")
	c1()

	c2 := IsolateTmuxSocket()
	dir2 := os.Getenv("TMUX_TMPDIR")
	c2()

	if dir1 == dir2 {
		t.Errorf("expected unique dirs per call, both got %q", dir1)
	}
}
