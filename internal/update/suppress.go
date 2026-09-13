package update

import (
	"os"
	"strings"
	"testing"

	"golang.org/x/term"
)

// Where the unattended paths must stay quiet.
//
// auto_install and auto_restart are for a person's deck. A TUI or daemon
// that a test, a CI job, an eval harness or a script started must never
// install a release that lands while it runs, and must never re-exec
// itself under the caller's feet (issue #2251: a required CI check failed
// twice because the TUI under test updated and restarted itself mid-test).
// Every automatic path asks AutoUpdateSuppressed first; the explicit
// commands (`agent-deck update`, `--unattended`) stay explicit.

// TestMarkerPrefix is the prefix of the environment markers the test suite
// sets on binaries it spawns (AGENTDECK_TEST_ARGV_LOG, ...). Any of them
// means "a test is driving this process".
const TestMarkerPrefix = "AGENTDECK_TEST_"

// AutoUpdateSuppressed reports why no automatic install or restart may run
// in this process, or "" when it may. It covers every process kind; the TUI
// adds the terminal check through TUIAutoUpdateSuppressed.
func AutoUpdateSuppressed() string {
	return autoUpdateSuppressed(os.Environ(), testing.Testing()) //nolint:forbidigo // read-only scan of our own env for markers; no child is launched (#1163 is about child env)
}

// TUIAutoUpdateSuppressed is AutoUpdateSuppressed plus the rule that the
// TUI's automatic paths need a real terminal on both stdin and stdout: a
// pipe or redirect on either side means a script or harness is driving.
func TUIAutoUpdateSuppressed() string {
	return tuiAutoUpdateSuppressed(os.Environ(), testing.Testing(), //nolint:forbidigo // read-only scan, see AutoUpdateSuppressed
		term.IsTerminal(int(os.Stdin.Fd())), term.IsTerminal(int(os.Stdout.Fd())))
}

// autoUpdateSuppressed is the pure decision over an environment vector and
// the go-test flag, in the order a reader would check them.
func autoUpdateSuppressed(env []string, underGoTest bool) string {
	if underGoTest {
		return "running under go test"
	}
	for _, kv := range env {
		key, value, _ := strings.Cut(kv, "=")
		switch {
		case key == SkipUpdateCheckEnv && envTruthy(value):
			return SkipUpdateCheckEnv + " set"
		case key == "CI" && envTruthy(value):
			return "CI=" + value
		case strings.HasPrefix(key, TestMarkerPrefix):
			return key + " set"
		}
	}
	return ""
}

// tuiAutoUpdateSuppressed adds the terminal rule for the TUI paths.
func tuiAutoUpdateSuppressed(env []string, underGoTest, stdinTTY, stdoutTTY bool) string {
	if reason := autoUpdateSuppressed(env, underGoTest); reason != "" {
		return reason
	}
	switch {
	case !stdinTTY && !stdoutTTY:
		return "stdin and stdout are not a terminal"
	case !stdinTTY:
		return "stdin is not a terminal"
	case !stdoutTTY:
		return "stdout is not a terminal"
	}
	return ""
}

// envTruthy mirrors isUpdateCheckSkipped: anything but an explicit off
// value counts as set.
func envTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}
