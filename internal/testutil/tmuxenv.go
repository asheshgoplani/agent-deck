package testutil

import (
	"fmt"
	"os"
)

// Name of the marker env var set during test isolation. Runtime guards in
// internal/tmux use this to detect a test context so they can panic loudly
// on an isolation leak instead of silently attacking the user's real
// tmux server.
const TestIsolationMarkerEnv = "AGENT_DECK_TEST_ISOLATED"

// IsolateTmuxSocket makes it safe to spawn real tmux servers from tests
// even when `go test` is invoked from inside a live tmux session (the
// default on every developer host that uses agent-deck).
//
// The helper does THREE things:
//
//  1. Unsets TMUX and TMUX_PANE. Tmux's client discovery order is:
//     `$TMUX → -S path → -L name → $TMUX_TMPDIR`. If TMUX is set, every
//     later step is ignored — so setting TMUX_TMPDIR alone provides zero
//     isolation when the test process inherits TMUX from a parent tmux
//     pane. This was the 2026-04-17 three-cascade bug: v1.7.3 set
//     TMUX_TMPDIR but left TMUX set, so every test-spawned tmux session
//     joined the user's real server and eventually destabilised it.
//
//  2. Sets TMUX_TMPDIR to a fresh per-call temp dir. Tests that use
//     `-L <name>` or `$TMUX_TMPDIR`-derived sockets will land here,
//     never at /tmp/tmux-<uid>/default.
//
//  3. Sets AGENT_DECK_TEST_ISOLATED=1. Production code paths in
//     internal/tmux read this marker at tmux-spawn time and panic with
//     a clear message if TMUX is still set and points to a non-isolated
//     socket — the "make the failure loud, not silent" belt to the
//     TMUX-unset suspender.
//
// Call this from every package-level TestMain that transitively spawns
// tmux:
//
//	func TestMain(m *testing.M) {
//	    cleanup := testutil.IsolateTmuxSocket()
//	    defer cleanup()
//	    os.Exit(m.Run())
//	}
//
// Returns a cleanup function that removes the temp dir and restores
// the original TMUX / TMUX_PANE / AGENT_DECK_TEST_ISOLATED values so
// the parent process's env is not permanently altered.
func IsolateTmuxSocket() func() {
	// Snapshot originals for cleanup-time restore.
	origTmux, hadTmux := os.LookupEnv("TMUX")
	origTmuxPane, hadTmuxPane := os.LookupEnv("TMUX_PANE")
	origTmuxTmpdir, hadTmuxTmpdir := os.LookupEnv("TMUX_TMPDIR")
	origMarker, hadMarker := os.LookupEnv(TestIsolationMarkerEnv)

	// CRITICAL: unset BEFORE setting TMUX_TMPDIR. TMUX takes precedence
	// in tmux client discovery, so leaving it set makes TMUX_TMPDIR
	// ignored. This single line is the 2026-04-17 fix.
	_ = os.Unsetenv("TMUX")
	_ = os.Unsetenv("TMUX_PANE")

	dir, err := os.MkdirTemp(shortTmuxTmpBase(), "ad-tmux-")
	if err != nil {
		// If we can't isolate via MkdirTemp, we still want tests to
		// run — but we REALLY don't want them on the default socket.
		// Fall back to a PID-keyed path that won't collide with other
		// test runs or the user's real sessions.
		dir = fmt.Sprintf("/tmp/agent-deck-test-tmux-fallback-%d", os.Getpid())
		_ = os.MkdirAll(dir, 0o700)
	}
	_ = os.Setenv("TMUX_TMPDIR", dir)
	_ = os.Setenv(TestIsolationMarkerEnv, "1")

	return func() {
		restoreEnv("TMUX", origTmux, hadTmux)
		restoreEnv("TMUX_PANE", origTmuxPane, hadTmuxPane)
		restoreEnv("TMUX_TMPDIR", origTmuxTmpdir, hadTmuxTmpdir)
		restoreEnv(TestIsolationMarkerEnv, origMarker, hadMarker)
		// Best-effort dir cleanup. Stale tmux sockets are harmless —
		// the kernel removes them when the bound tmux server exits.
		_ = os.RemoveAll(dir)
	}
}

// shortTmuxTmpBase returns a short base directory for the per-test TMUX_TMPDIR.
//
// UNIX-domain socket paths are capped at sockaddr_un.sun_path (104 chars on
// darwin, 108 on linux). tmux appends "/tmux-<uid>/<sock>" (~17 chars) to
// TMUX_TMPDIR, and MkdirTemp's random suffix eats ~10 more, so the base must
// stay well under ~75 chars. On darwin, os.TempDir() returns
// /var/folders/<aa>/<32-char-hash>/T (resolved through /private/...) which is
// ~56 chars and immediately overshoots the limit. /tmp is the well-known short
// path on every Unix-like OS, matches tmux's own default location, and matches
// the existing failure-mode fallback at the bottom of IsolateTmuxSocket.
//
// Returns "/tmp" when writable; otherwise returns "" so os.MkdirTemp falls
// back to os.TempDir() (preserving the prior behavior on hosts that remap
// TMPDIR but leave /tmp unwritable — rare under sandboxes/SELinux/AppArmor).
func shortTmuxTmpBase() string {
	const candidate = "/tmp"
	info, err := os.Stat(candidate)
	if err != nil || !info.IsDir() {
		return ""
	}
	probe, err := os.CreateTemp(candidate, ".ad-tmux-probe-")
	if err != nil {
		return ""
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return candidate
}

// restoreEnv puts an env var back to its original state. If it wasn't set
// before IsolateTmuxSocket ran, unset it; otherwise set it to the original.
func restoreEnv(key, orig string, had bool) {
	if had {
		_ = os.Setenv(key, orig)
	} else {
		_ = os.Unsetenv(key)
	}
}
