package telemetry

import (
	"os"
	"testing"
)

// EnableForTest lifts the test-binary hard-off for the calling test only.
// Every other hard-off (env, config, CI, TTY) still applies, and the test
// must isolate HOME itself. Without it, telemetry in a test binary records,
// prompts and sends nothing.
func EnableForTest(t testing.TB) {
	t.Helper()
	prev := testAllowed
	testAllowed = true
	t.Cleanup(func() { testAllowed = prev })
}

// SetTerminalForTest pins the stdin/stdout TTY check for the calling test.
func SetTerminalForTest(t testing.TB, tty bool) {
	t.Helper()
	prev := isTerminalFn
	isTerminalFn = func() bool { return tty }
	t.Cleanup(func() { isTerminalFn = prev })
}

// ClearCIForTest unsets every CI marker for the calling test, so a test that
// needs a person at a terminal passes on CI runners (GitHub sets
// GITHUB_ACTIONS, not only CI). Set-but-empty counts as CI, so each marker is
// unset; t.Setenv restores the original value on cleanup.
func ClearCIForTest(t testing.TB) {
	t.Helper()
	for _, k := range ciMarkers {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}
