package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// Field report: a switch-account that exited 1 left no readable reason once
// its output went through a filter. Every non-zero exit prints one "Error:"
// line on stderr, in text and in --json mode.
func TestSessionSwitchAccount_NonZeroExitPrintsOneLineReasonOnStderr(t *testing.T) {
	home := intendedSwitchHome(t)
	id := addTestSession(t, home, filepath.Join(home, "proj"), "worker")
	cases := map[string][]string{
		"unknown account":      {"session", "switch-account", id, "nosuch"},
		"unknown account json": {"session", "switch-account", id, "nosuch", "--json"},
		"unknown session json": {"session", "switch-account", "no-such-session", "work", "--json"},
		"missing account":      {"session", "switch-account", id},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			_, stderr, code := runAgentDeck(t, home, args...)
			if code == 0 {
				t.Fatalf("exit 0, want a failure")
			}
			var reasons []string
			for _, line := range strings.Split(stderr, "\n") {
				if strings.HasPrefix(line, "Error: ") {
					reasons = append(reasons, line)
				}
			}
			if len(reasons) != 1 || len(reasons[0]) <= len("Error: ") {
				t.Fatalf("stderr reasons = %q, want exactly one non-empty \"Error:\" line\nstderr:\n%s", reasons, stderr)
			}
		})
	}
}
