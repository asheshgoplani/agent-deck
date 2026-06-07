package profile

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestMain isolates HOME+XDG for the whole package BEFORE any test runs.
//
// WHY (2026-06-07 release-gate failure):
//
// internal/profile had no TestMain, so it inherited the bare CI environment
// (HOME=/home/runner, XDG_* unset). The precedence tests then swap HOME via
// t.Setenv to a per-test temp dir and seed config.json there. That works
// locally, but in CI the S4 path-safety guard (agentpaths.ensureSafeForTest)
// compares resolved paths against the OS user's *passwd* home — which
// t.Setenv does NOT change — and when any path resolved under /home/runner it
// failed closed. EffectiveConfigPath then returned an error, LoadConfig
// errored, and GetEffectiveProfile fell back to the literal "default" — so
// TestProfileResolution_ConfigDefaultFallback and two PrecedenceLadder
// subtests saw "default" instead of the seeded config default_profile.
//
// IsolateHome points the package HOME at a fresh /tmp dir (off the passwd
// home) and clears every XDG base dir so they track HOME. Per-test
// t.Setenv("HOME", t.TempDir()) then resolves cleanly under /tmp and the
// guard never trips. Mirrors the isolation 98730b2a added to
// internal/session and cmd/agent-deck.
func TestMain(m *testing.M) {
	cleanupHome := testutil.IsolateHome()
	// Isolate the tmux socket too: required by TestAllTestMainsIsolateTmuxSocket
	// so `go test ./...` on a host running agent-deck can't spawn sessions on
	// the user's default socket (2026-04-17 incident).
	cleanupTmux := testutil.IsolateTmuxSocket()
	code := m.Run()
	cleanupTmux()
	cleanupHome()
	os.Exit(code)
}
