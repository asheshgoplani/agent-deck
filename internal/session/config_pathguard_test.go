package session

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

// S4 data-loss safeguard tests.
//
// Chain-link #2 of the 2026-06-04 incident: path resolution silently landed
// under the real home whenever HOME pointed at the real OS-user home (e.g. a
// test that forgot testutil.IsolateHome()). No signal was emitted, so an
// un-isolated test silently touched live user data.
//
// S4 closes the gap inside the production resolver itself:
//   - under test (testing.Testing()==true) agentpaths REFUSES (returns an
//     error) when resolution lands under the real OS-user home;
//   - the real binary's behavior is unchanged.
//
// These tests build on S1 (statedb) and S5 (testutil.IsolateHome + pathsafety
// guard). The package TestMain already calls IsolateHome(), so the sandboxed
// happy-path is the default; the real-home cases simulate the un-isolated
// failure by pointing HOME back at the real home.

// osRealHome returns the developer's actual home directory from the OS user
// database, independent of $HOME. Mirrors internal/pathsafety's realHome().
func osRealHome(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil || u.HomeDir == "" {
		t.Skip("cannot determine real home directory from OS user database")
	}
	return filepath.Clean(u.HomeDir)
}

// TestGetAgentDeckDir_SandboxedResolvesToXDGData confirms the normal,
// sandboxed path returns the XDG data dir under the isolated HOME.
func TestGetAgentDeckDir_SandboxedResolvesToXDGData(t *testing.T) {
	// TestMain already isolated HOME. Sanity-check we are not on the real home.
	home, _ := os.UserHomeDir()
	if home == osRealHome(t) {
		t.Fatalf("precondition: HOME (%s) must be sandboxed, not the real home", home)
	}

	dir, err := GetAgentDeckDir()
	if err != nil {
		t.Fatalf("GetAgentDeckDir under sandbox returned error: %v", err)
	}
	want := filepath.Join(os.Getenv("XDG_DATA_HOME"), "agent-deck")
	if filepath.Clean(dir) != filepath.Clean(want) {
		t.Fatalf("GetAgentDeckDir = %s, want %s", dir, want)
	}
}

// TestGetAgentDeckDir_RefusesUnderTestOnRealHome is the core S4 guard. When the
// resolver would land under the real OS-user home WHILE running under test, it
// must return an error rather than silently handing back the live path.
func TestGetAgentDeckDir_RefusesUnderTestOnRealHome(t *testing.T) {
	real := osRealHome(t)

	// Simulate an un-isolated test: point HOME back at the real home and clear
	// the XDG data override that TestMain normally provides.
	t.Setenv("HOME", real)
	t.Setenv("XDG_DATA_HOME", "")

	dir, err := GetAgentDeckDir()
	if err == nil {
		t.Fatalf("expected refusal error when resolving under real home, got dir=%s nil error", dir)
	}
	if !strings.Contains(err.Error(), "real home") {
		t.Fatalf("error should mention the real-home guard; got: %v", err)
	}
}

// TestGetConfigPath_RefusesUnderTestOnRealHome proves the refusal propagates
// through the dependent resolvers (GetConfigPath builds on GetAgentDeckDir).
func TestGetConfigPath_RefusesUnderTestOnRealHome(t *testing.T) {
	real := osRealHome(t)
	t.Setenv("HOME", real)
	t.Setenv("XDG_CONFIG_HOME", "")

	if p, err := GetConfigPath(); err == nil {
		t.Fatalf("GetConfigPath should refuse under real home; got %s nil error", p)
	}
}
