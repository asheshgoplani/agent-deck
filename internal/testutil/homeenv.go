package testutil

import (
	"fmt"
	"os"
	"path/filepath"
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
// IsolateHome closes that gap by pointing HOME and every XDG_* base dir at a
// fresh per-call temp dir, and setting AGENTDECK_PROFILE=_test as a second
// belt. It mirrors IsolateTmuxSocket (internal/testutil/tmuxenv.go).
//
// It sets:
//   - HOME             -> <tempdir>            (os.UserHomeDir source on Unix)
//   - XDG_CONFIG_HOME  -> <tempdir>/.config
//   - XDG_DATA_HOME    -> <tempdir>/.local/share
//   - XDG_CACHE_HOME   -> <tempdir>/.cache
//   - XDG_STATE_HOME   -> <tempdir>/.local/state
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
	_ = os.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))
	_ = os.Setenv("XDG_DATA_HOME", filepath.Join(dir, ".local", "share"))
	_ = os.Setenv("XDG_CACHE_HOME", filepath.Join(dir, ".cache"))
	_ = os.Setenv("XDG_STATE_HOME", filepath.Join(dir, ".local", "state"))
	_ = os.Setenv("AGENTDECK_PROFILE", "_test")
	_ = os.Setenv(HomeIsolationMarkerEnv, "1")

	return func() {
		for _, s := range snaps {
			restoreEnv(s.key, s.val, s.had)
		}
		_ = os.RemoveAll(dir)
	}
}
