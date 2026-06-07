package testutil

import (
	"fmt"
	"os"
)

// HomeIsolationMarkerEnv is set during HOME+XDG isolation. Runtime guards (and
// the pathsafety guard test) read this to confirm a test context is sandboxed.
const HomeIsolationMarkerEnv = "AGENT_DECK_TEST_HOME_ISOLATED"

// IsolateHome makes it safe for tests to resolve and write agent-deck runtime
// paths (~/.agent-deck/config.json, profiles/<p>/state.db, worker-scratch,
// logs, hooks) without ever touching the developer's real home directory.
//
// WHY THIS EXISTS (2026-06-04 data-loss incident — third of its class):
//
// `go test` resolves runtime paths via the HOME env var (os.UserHomeDir reads
// $HOME on Unix), NOT via the test's working directory. So running the suite
// from inside a git worktree still wrote to the real ~/.agent-deck. Test
// isolation up to that point relied solely on AGENTDECK_PROFILE=_test, which
// only scopes the *profile subdirectory* — GetAgentDeckDir(), config.json,
// worker-scratch/, and logs/ all still resolved under the real HOME. The
// concrete trigger: internal/ui's TestMain set AGENTDECK_PROFILE=_test but did
// NOT override HOME/XDG, so an un-sandboxed `go test ./internal/ui/...` wiped
// the live profile index + config.
//
// IsolateHome closes that gap by pointing HOME at a fresh per-call temp dir,
// CLEARING every XDG_* base dir so they track HOME, and setting
// AGENTDECK_PROFILE=_test as a second belt. It mirrors IsolateTmuxSocket
// (internal/testutil/tmuxenv.go).
//
// WHY XDG IS CLEARED, NOT PINNED (2026-06-07 ~96-test isolation regression):
//
// IsolateHome runs once per package (from TestMain), so its temp dir is SHARED
// by every test in the package. After #1294's XDG refactor, path resolution
// prefers an XDG location *if the file already exists* on disk. Pinning
// XDG_CONFIG_HOME to <pkg-tempdir>/.config meant one test writing config.toml
// there left a stale file that LATER tests then read instead of their own
// fresh slate — config bled across tests (catalog "empty", profile resolving
// to `default`, ~96 cross-package failures). Worse, the many tests that swap
// only HOME via t.TempDir() left XDG_CONFIG_HOME still pointing at the shared
// package dir, so their per-test config redirection silently did nothing.
//
// By clearing XDG_* instead, every base dir falls back to $HOME/.config,
// $HOME/.local/share, etc. (see agentpaths.xdgDir). Now any test that swaps
// HOME — which is the overwhelmingly common pattern — automatically gets a
// fully isolated, clean config/data/cache slate, with zero per-test wiring.
// Tests that genuinely need a specific XDG dir still set it explicitly via
// t.Setenv (auto-restored), which takes precedence.
//
// It sets:
//   - HOME             -> <tempdir>            (os.UserHomeDir source on Unix)
//   - XDG_CONFIG_HOME  -> ""  (cleared; resolves under $HOME/.config)
//   - XDG_DATA_HOME    -> ""  (cleared; resolves under $HOME/.local/share)
//   - XDG_CACHE_HOME   -> ""  (cleared; resolves under $HOME/.cache)
//   - XDG_STATE_HOME   -> ""  (cleared; resolves under $HOME/.local/state)
//   - AGENTDECK_PROFILE -> _test
//   - AGENT_DECK_TEST_HOME_ISOLATED -> 1  (marker for guard/runtime checks)
//
// Call it from every package-level TestMain that transitively resolves an
// agent-deck path:
//
//	func TestMain(m *testing.M) {
//	    cleanupHome := testutil.IsolateHome()
//	    defer cleanupHome()
//	    cleanupTmux := testutil.IsolateTmuxSocket()
//	    defer cleanupTmux()
//	    os.Exit(m.Run())
//	}
//
// Returns a cleanup function that removes the temp dir and restores the
// original env so the parent process is not permanently altered.
func IsolateHome() func() {
	type snap struct {
		key string
		val string
		had bool
	}

	keys := []string{
		"HOME",
		"XDG_CONFIG_HOME",
		"XDG_DATA_HOME",
		"XDG_CACHE_HOME",
		"XDG_STATE_HOME",
		"AGENTDECK_PROFILE",
		HomeIsolationMarkerEnv,
	}

	snaps := make([]snap, 0, len(keys))
	for _, k := range keys {
		v, had := os.LookupEnv(k)
		snaps = append(snaps, snap{key: k, val: v, had: had})
	}

	dir, err := os.MkdirTemp("", "ad-home-")
	if err != nil {
		// We must never fall back to the real HOME. A PID-keyed path under
		// /tmp is still safely off the real home.
		dir = fmt.Sprintf("/tmp/agent-deck-test-home-fallback-%d", os.Getpid())
		_ = os.MkdirAll(dir, 0o700)
	}

	_ = os.Setenv("HOME", dir)
	// Clear (do NOT pin) the XDG base dirs so they fall back to $HOME/*. This
	// keeps the per-package shared HOME from accumulating stale XDG config/data
	// across tests, and lets the common "swap HOME via t.TempDir()" pattern
	// isolate config automatically. See the doc comment above.
	_ = os.Unsetenv("XDG_CONFIG_HOME")
	_ = os.Unsetenv("XDG_DATA_HOME")
	_ = os.Unsetenv("XDG_CACHE_HOME")
	_ = os.Unsetenv("XDG_STATE_HOME")
	_ = os.Setenv("AGENTDECK_PROFILE", "_test")
	_ = os.Setenv(HomeIsolationMarkerEnv, "1")

	return func() {
		for _, s := range snaps {
			restoreEnv(s.key, s.val, s.had)
		}
		_ = os.RemoveAll(dir)
	}
}
