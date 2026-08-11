package testutil

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
)

// HomeIsolationMarkerEnv is set during HOME+XDG isolation. Runtime guards (and
// the pathsafety guard test) read this to confirm a test context is sandboxed.
const HomeIsolationMarkerEnv = "AGENT_DECK_TEST_HOME_ISOLATED"

// HomeAlreadyIsolated reports whether the current process INHERITED a HOME that
// IsolateHome/IsolatePackageHome already sandboxed. The marker env var is set
// only by those two helpers, so its presence is proof the environment is off the
// real home.
//
// Use it in a TestMain to skip re-isolation in a re-exec'd helper subprocess —
// the `exec.Command(os.Args[0], "-test.run=TestSomeHelperProcess")` pattern.
// Such a helper calls os.Exit to exercise a production exit path, which means
// TestMain's deferred cleanup never runs; if it also isolated, its temp HOME
// would be stranded on every invocation (2026-08-10 temp-leak incident: the
// cursor-hooks helper stranded one empty dir per call). Skipping is not a
// safety trade — the inherited HOME is already sandboxed, and re-isolating
// would additionally clobber env the parent deliberately staged for the child.
// THE MARKER ALONE IS NOT TRUSTED. Because callers use this to DISABLE
// isolation, a marker left exported in a developer's shell would otherwise run
// a whole suite against the real ~/.agent-deck — the 2026-06-04 data-loss
// incident, re-armed. So the sandbox claim is corroborated against the passwd
// database via os/user, which reports the account's home independently of $HOME
// (os.UserHomeDir merely reads $HOME on Unix and would agree with any lie). A
// marker claiming isolation while HOME still points at the real home is treated
// as NOT isolated, and the caller isolates as usual. Isolating redundantly is
// wasteful; skipping it wrongly destroys user data.
func HomeAlreadyIsolated() bool {
	if os.Getenv(HomeIsolationMarkerEnv) != "1" {
		return false
	}
	home := os.Getenv("HOME")
	if home == "" {
		return false
	}
	u, err := user.Current()
	if err != nil || u.HomeDir == "" {
		// Cannot corroborate the claim; refuse to disable isolation on it.
		return false
	}
	return filepath.Clean(home) != filepath.Clean(u.HomeDir)
}

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
	return IsolatePackageHome("ad-home-")
}

// IsolatePackageHome is IsolateHome with a caller-chosen os.MkdirTemp pattern,
// so a package's temp HOME is identifiable in $TMPDIR by name. Everything else
// — the env snapshot, the cleared XDG bases, the _test profile, the isolation
// marker, and the cleanup contract — is identical; see IsolateHome above.
//
// WHY THIS REPLACED PER-PACKAGE COPIES (2026-08-10 temp-leak incident):
//
// cmd/agent-deck and internal/session each carried a private isolatePackageHome
// helper that called os.MkdirTemp and set HOME with NO cleanup whatsoever, on
// top of an already-isolated IsolateHome dir. Two consequences, both bad:
//
//  1. Every `go test` run stranded a temp HOME forever. Because those packages
//     shell out to `go build`, each stranded HOME held a full Go module and
//     build cache — ~1.5 GB a run. 630 such directories, ~100 GB, had piled up
//     in $TMPDIR by the time this was found.
//  2. Overwriting HOME after IsolateHome meant IsolateHome's cleanup deleted a
//     directory nothing had used, while the directory that actually held the
//     package's state was the one with no owner.
//
// Folding the prefix into this one helper leaves a single temp HOME per
// package with a single owner that removes it.
func IsolatePackageHome(pattern string) func() {
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

	// Clear leftovers from earlier runs of THIS package's prefix that were
	// killed before their cleanup could run. Bounded to os.TempDir(), to the
	// exact owned prefix, and to dirs older than StaleTempDirAge — far longer
	// than any test run, so a concurrent suite's live HOME is never touched.
	// See ReapStaleTempDirs in tempcleanup.go.
	ReapStaleTempDirs(os.TempDir(), pattern, StaleTempDirAge)

	dir, err := os.MkdirTemp("", pattern)
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
		// NOT os.RemoveAll: a subprocess `go build` run under this HOME leaves
		// a read-only module cache that plain RemoveAll cannot delete, and the
		// discarded error hid the resulting leak. See RemoveTempTree.
		reportTempTreeRemoval("isolated HOME", dir)
	}
}
