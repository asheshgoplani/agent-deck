package telemetry

import "testing"

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
