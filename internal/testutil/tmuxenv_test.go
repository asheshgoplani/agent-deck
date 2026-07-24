package testutil

import (
	"os"
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

// TestIsolateTmuxSocket_RestoresOriginalsOnCleanup ensures the cleanup
// function puts the parent-process view of TMUX/TMUX_PANE back, so that
// `go test` does not permanently break the developer's interactive shell
// env if the test process leaks them somehow.
func TestIsolateTmuxSocket_RestoresOriginalsOnCleanup(t *testing.T) {
	origTmux, hadTmux := os.LookupEnv("TMUX")
	origPane, hadPane := os.LookupEnv("TMUX_PANE")
	origTmuxTmpdir, hadTmuxTmpdir := os.LookupEnv("TMUX_TMPDIR")
	defer func() {
		restore("TMUX", origTmux, hadTmux)
		restore("TMUX_PANE", origPane, hadPane)
		restore("TMUX_TMPDIR", origTmuxTmpdir, hadTmuxTmpdir)
	}()

	sentinelTmux := "/tmp/tmux-1000/default,98765,0"
	sentinelPane := "%99"
	os.Setenv("TMUX", sentinelTmux)
	os.Setenv("TMUX_PANE", sentinelPane)

	cleanup := IsolateTmuxSocket()

	// During isolation, both must be empty.
	if os.Getenv("TMUX") != "" || os.Getenv("TMUX_PANE") != "" {
		t.Fatal("during isolation, TMUX and TMUX_PANE must be unset")
	}

	cleanup()

	// After cleanup, both restored.
	if got := os.Getenv("TMUX"); got != sentinelTmux {
		t.Errorf("cleanup did not restore TMUX: got %q, want %q", got, sentinelTmux)
	}
	if got := os.Getenv("TMUX_PANE"); got != sentinelPane {
		t.Errorf("cleanup did not restore TMUX_PANE: got %q, want %q", got, sentinelPane)
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

// TestIsolatedTmuxDirUsable pins the guard added after a live incident
// (2026-07-24): `tmux kill-server` under an EMPTY TMUX_TMPDIR does not fail —
// it silently resolves to the real default tmpdir and kills the user's own
// tmux server, taking every session on it down at once with no recovery path.
//
// Asserted on the pure predicate rather than by observing that no server died,
// because the only way to observe the failure is to cause it.
func TestIsolatedTmuxDirUsable(t *testing.T) {
	// Every empty form must refuse. A bare "" is the shape the incident took
	// (a shell variable that came back empty); the whitespace forms are the
	// same class and cost nothing to cover.
	for _, dir := range []string{"", " ", "\t", "\n  ", "   \t\n"} {
		if isolatedTmuxDirUsable(dir) {
			t.Errorf("dir %q must be refused — an empty TMUX_TMPDIR retargets "+
				"kill-server at the user's real tmux server", dir)
		}
	}

	// A real private dir must still be honored, or cleanup silently leaks the
	// per-test server and its PTYs until reboot.
	for _, dir := range []string{"/tmp/ad-tmux-123", "/var/folders/x/y/T/ad-tmux-abc"} {
		if !isolatedTmuxDirUsable(dir) {
			t.Errorf("dir %q must be accepted", dir)
		}
	}
}
