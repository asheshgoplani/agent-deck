package tmux

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// testIsolationMarkerEnv is the marker set by internal/testutil.IsolateTmuxSocket.
// Kept as a string literal (not imported from testutil) so production builds
// do not pick up the testing package as a transitive dependency.
const testIsolationMarkerEnv = "AGENT_DECK_TEST_ISOLATED"

// assertTestTmuxIsolation is the third and loudest layer of defense against
// the 2026-04-17 three-cascade bug.
//
// What the bug was: tests spawned tmux servers that joined the user's real
// tmux server on /tmp/tmux-<uid>/default, because the process inherited the
// parent tmux pane's TMUX env var. This silently destabilised the shared
// server and killed every live agent-deck session. It happened three times
// in one day because the first two fixes patched the wrong surface
// (TMUX_TMPDIR), not the real one (TMUX).
//
// The chain of guards that should make this impossible now:
//
//  1. testutil.IsolateTmuxSocket unsets TMUX/TMUX_PANE, sets TMUX_TMPDIR to
//     an isolated dir, and sets AGENT_DECK_TEST_ISOLATED=1 as a marker.
//  2. A repo-level audit test verifies every TestMain that transitively
//     spawns tmux calls IsolateTmuxSocket.
//  3. THIS guard runs at every Session.Start and panics if:
//     - the process looks like a Go test binary (os.Args[0] ends in .test)
//     - AND TMUX is set AND points at the default-socket pattern
//     - AND the isolation marker is NOT set
//
// Why panic instead of silently failing or logging: the cascade takes
// seconds to kill 30 live sessions. A panic fails the test immediately and
// LOUDLY, before any tmux subprocess is spawned. The cost of a false panic
// is one confused developer; the cost of a silent failure is losing hours
// of work across every active session.
func assertTestTmuxIsolation() {
	if !looksLikeGoTestBinary() {
		return
	}

	// If the test harness correctly called IsolateTmuxSocket, the marker
	// is set and TMUX/TMUX_PANE are unset. Nothing to do.
	if os.Getenv(testIsolationMarkerEnv) == "1" {
		// Belt + suspenders: even with the marker, re-verify that TMUX
		// is unset. If it is set, someone manually set it after
		// IsolateTmuxSocket, which would re-introduce the bug.
		if tmuxEnv := os.Getenv("TMUX"); tmuxEnv != "" && looksLikeUserDefaultSocket(tmuxEnv) {
			panic(fmt.Sprintf(
				"tmux isolation guard: test process has AGENT_DECK_TEST_ISOLATED=1 "+
					"but TMUX=%q points at the user's default socket. Something re-set "+
					"TMUX after testutil.IsolateTmuxSocket ran. Refusing to spawn tmux "+
					"to prevent the 2026-04-17 cascade bug.", tmuxEnv))
		}
		return
	}

	// No marker: either TestMain never called IsolateTmuxSocket, or this
	// code path is being driven outside a proper TestMain (e.g. an ad-hoc
	// benchmark, an external tool linking against internal/tmux). If TMUX
	// happens to point at the user's default socket, panic — this is
	// exactly the condition that killed every session three times today.
	tmuxEnv := os.Getenv("TMUX")
	if tmuxEnv == "" {
		// NOT safe, despite what this branch used to claim ("no inherited
		// socket to leak onto"). tmux finds the user's live server without
		// $TMUX: with no -S, no -L and no TMUX_TMPDIR it resolves to
		// <tmpdir>/tmux-<uid>/default all by itself. That blind spot is why
		// assertTmuxSpawnIsolated (default_socket_guard.go) now refuses at the
		// argv factory based on the RESOLVED socket path, which covers this
		// path and every other spawn in the package. The warning stays as the
		// nudge toward proper package isolation.
		slog.Warn("tmux_isolation_guard_no_marker_but_tmux_unset",
			slog.String("hint", "test binary did not set AGENT_DECK_TEST_ISOLATED=1; "+
				"call testutil.IsolateTmuxSocket() from TestMain to enable the full guard"))
		return
	}
	if looksLikeUserDefaultSocket(tmuxEnv) {
		panic(fmt.Sprintf(
			"tmux isolation guard: test binary %q is about to spawn a tmux session, "+
				"but TMUX=%q is inherited from the user's real tmux pane and "+
				"AGENT_DECK_TEST_ISOLATED is not set. This is the exact condition "+
				"that caused the 2026-04-17 three-cascade bug — refusing to proceed. "+
				"FIX: add `cleanup := testutil.IsolateTmuxSocket(); defer cleanup()` "+
				"to this package's TestMain.",
			os.Args[0], tmuxEnv))
	}
}

// looksLikeGoTestBinary returns true when the running process was compiled
// by `go test` (test binary names end in `.test` or contain `_test/` in
// the exec path).
func looksLikeGoTestBinary() bool {
	if len(os.Args) == 0 {
		return false
	}
	arg0 := os.Args[0]
	if strings.HasSuffix(arg0, ".test") || strings.HasSuffix(arg0, ".test.exe") {
		return true
	}
	// Test binaries produced by `go test -c` live under `/tmp/go-build*/`
	// and have names ending in `.test`. Cover that explicitly.
	if strings.Contains(arg0, "/go-build") && strings.Contains(arg0, ".test") {
		return true
	}
	return false
}

// looksLikeUserDefaultSocket reports whether the TMUX env value names a
// path under /tmp/tmux-<uid>/ — the default tmux server location used by
// every interactive tmux pane. The check is intentionally broad: any
// match triggers the panic, so a false positive is preferable to a false
// negative.
//
// The TMUX env var format is "<socket-path>,<server-pid>,<session-id>".
func looksLikeUserDefaultSocket(tmuxEnv string) bool {
	// Only the first comma-separated field matters.
	path := tmuxEnv
	if comma := strings.IndexByte(path, ','); comma >= 0 {
		path = path[:comma]
	}
	// The default location. Use a prefix check so we catch any UID and
	// any socket name under that dir (`default`, `default.old`, etc).
	return strings.HasPrefix(path, "/tmp/tmux-")
}
