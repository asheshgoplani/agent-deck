package testutil

import (
	"os"
	"os/exec"
	"testing"
)

// Sandbox skips (issue #2937). Distro builders such as the nix package run a
// plain `go test` in a build sandbox: tmux may be absent, HOME may be
// read-only or missing, there is no network and few CPUs. Tests that need
// the real tmux binary, a writable HOME, or a long end-to-end run call one
// of these helpers so they skip with a clear reason there. CI runs without
// -short, with tmux and a writable HOME, so they still run in CI.

// RequireTmux skips t when no tmux binary is on PATH.
func RequireTmux(t testing.TB) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("skipping: tmux not on PATH (sandboxed build?)")
	}
}

// RequireWritableHome skips t when $HOME is unset, missing, or not writable.
func RequireWritableHome(t testing.TB) {
	t.Helper()
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("skipping: HOME is not set (sandboxed build?)")
	}
	dir, err := os.MkdirTemp(home, ".agent-deck-home-probe-")
	if err != nil {
		t.Skipf("skipping: HOME %q is not writable (sandboxed build?): %v", home, err)
	}
	_ = os.Remove(dir)
}

// SkipShort skips t under `go test -short`, naming why the test is long.
// (SkipIfShort in perfbudget.go is the older perf-only variant.)
func SkipShort(t testing.TB, why string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping in -short mode: " + why)
	}
}
