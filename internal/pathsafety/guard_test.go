// Package pathsafety holds the S5 data-loss guard test.
//
// It is the single most important artifact of the 2026-06-04 data-loss
// safeguard: it resolves agent-deck's real runtime path functions and FAILS
// LOUDLY if any of them resolve under the developer's real home directory or
// the live ~/.agent-deck. If someone runs the suite WITHOUT the mandatory
// HOME+XDG sandbox, this test fails fast and visibly — instead of `go test`
// silently wiping the live profile index + config (which is exactly what
// happened on 2026-06-04, the third incident of its class).
//
// Background: `go test` resolves runtime paths via $HOME, not the test's cwd,
// so even running from a git worktree wrote to the real ~/.agent-deck. The
// previous isolation relied only on AGENTDECK_PROFILE=_test, which scopes a
// profile subdir but leaves config.json / worker-scratch / logs / the base
// dir itself resolving under the real HOME.
package pathsafety

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

// runTestMain holds the real TestMain body so the deferred HOME-restore below
// actually runs: TestMain calls os.Exit, which does NOT run deferred functions,
// so registering the defer here and returning the exit code is the only way to
// guarantee the temp dir is removed (2026-06-07 pty-exhaustion incident class).
func runTestMain(m *testing.M) int {
	// This package's OWN tests must be sandboxed too — IsolateHome points
	// HOME+XDG at a temp dir. The guard below then proves the sandbox is real
	// by comparing resolved paths against the OS-level real home (read from
	// the user database, which IsolateHome does NOT touch).
	cleanupHome := testutil.IsolateHome()
	defer cleanupHome()
	return m.Run()
}

// realHome returns the developer's actual home directory, independent of the
// $HOME env var (which IsolateHome overrides). It reads the OS user database so
// the guard can tell a sandboxed path from a real-home path even after HOME is
// pointed elsewhere. Falls back to a best-effort env read only if the user
// database is unavailable.
func realHome(t *testing.T) string {
	t.Helper()
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		return filepath.Clean(u.HomeDir)
	}
	// Last resort: SUDO_USER / LOGNAME-based lookup is overkill here; if the
	// user DB is unreadable we can't reliably know the real home, so skip
	// rather than emit a false positive/negative.
	t.Skip("cannot determine real home directory from OS user database")
	return ""
}

// resolvedPaths gathers every agent-deck runtime path the production code
// computes. These are the exact sinks that a `go test` write would corrupt.
func resolvedPaths(t *testing.T) map[string]string {
	t.Helper()
	paths := map[string]string{}

	if p, err := session.GetAgentDeckDir(); err == nil {
		paths["AgentDeckDir"] = p
	} else {
		t.Fatalf("GetAgentDeckDir: %v", err)
	}
	if p, err := session.GetConfigPath(); err == nil {
		paths["ConfigPath"] = p
	} else {
		t.Fatalf("GetConfigPath: %v", err)
	}
	if p, err := session.GetProfilesDir(); err == nil {
		paths["ProfilesDir"] = p
	} else {
		t.Fatalf("GetProfilesDir: %v", err)
	}
	if p, err := session.GetDBPathForProfile(session.DefaultProfile); err == nil {
		paths["StateDBPath"] = p
	} else {
		t.Fatalf("GetDBPathForProfile: %v", err)
	}
	if p, err := session.GetUserConfigPath(); err == nil {
		paths["UserConfigPath"] = p
	} else {
		t.Fatalf("GetUserConfigPath: %v", err)
	}
	if p, err := session.WorkerScratchDirRoot(); err == nil {
		paths["WorkerScratchRoot"] = p
	} else {
		t.Fatalf("WorkerScratchDirRoot: %v", err)
	}

	return paths
}

// TestPathsDoNotResolveUnderRealHome is the S5 guard. It FAILS if any
// agent-deck runtime path resolves under the developer's real home directory.
//
// Run it sandboxed and it passes (paths land in the temp HOME). Run the suite
// WITHOUT the HOME+XDG sandbox and this test fails fast — turning a silent
// data-loss into a loud, immediate test failure.
func TestPathsDoNotResolveUnderRealHome(t *testing.T) {
	home := realHome(t)
	realAgentDeck := filepath.Join(home, ".agent-deck")

	for name, p := range resolvedPaths(t) {
		clean := filepath.Clean(p)

		// 1. The path must not live under the real home directory.
		if clean == home || strings.HasPrefix(clean, home+string(os.PathSeparator)) {
			t.Errorf(
				"DATA-LOSS GUARD TRIPPED: %s resolved under the real home directory.\n"+
					"  resolved: %s\n"+
					"  realHome: %s\n\n"+
					"The test suite is NOT sandboxed. Running it would write to (and can "+
					"WIPE) the live ~/.agent-deck. This is the 2026-06-04 incident class.\n"+
					"Run every test with a sandboxed HOME+XDG, e.g.:\n"+
					"  HOME=$(mktemp -d) XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME= go test -race ./<pkg>/...\n"+
					"and ensure the package's TestMain calls testutil.IsolateHome().",
				name, clean, home,
			)
			continue
		}

		// 2. Belt-and-suspenders: the path must not be the real ~/.agent-deck
		// even if (1) somehow missed it (e.g. symlinked home).
		if clean == realAgentDeck || strings.HasPrefix(clean, realAgentDeck+string(os.PathSeparator)) {
			t.Errorf(
				"DATA-LOSS GUARD TRIPPED: %s resolved inside the live ~/.agent-deck (%s).\n"+
					"  resolved: %s\n\n"+
					"Sandbox HOME+XDG before running tests (see testutil.IsolateHome()).",
				name, realAgentDeck, clean,
			)
		}
	}
}

// TestHomeIsActuallySandboxed proves the marker env is set and HOME points at a
// temp dir, so a future refactor of IsolateHome that silently stops working is
// caught here rather than in production.
func TestHomeIsActuallySandboxed(t *testing.T) {
	if os.Getenv(testutil.HomeIsolationMarkerEnv) != "1" {
		t.Fatalf("%s not set — IsolateHome did not run; the suite is un-sandboxed",
			testutil.HomeIsolationMarkerEnv)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("os.UserHomeDir: %v", err)
	}
	if home == realHome(t) {
		t.Fatalf("HOME (%s) equals the real home — IsolateHome failed to override it", home)
	}
}
