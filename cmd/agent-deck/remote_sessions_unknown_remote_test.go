package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `remote sessions <name>` for a remote that is not configured is an error
// on every output form, also when no remote is configured at all. It used to
// answer an empty list with exit 0 there, which reads as "that remote has no
// sessions".
func TestRemoteSessions_UnknownRemoteIsAnError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string
	}{
		{"no remotes configured", ""},
		{"another remote configured", "[remotes.lab]\nhost = 'lab.invalid'\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if tc.config != "" {
				dir := filepath.Join(home, ".config", "agent-deck")
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(tc.config), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, args := range [][]string{
				{"remote", "sessions", "nosuch"},
				{"remote", "sessions", "nosuch", "--json"},
			} {
				stdout, stderr, code := runAgentDeck(t, home, args...)
				if code != 1 || !strings.Contains(stdout+stderr, "remote 'nosuch' not found") || strings.TrimSpace(stdout) == "[]" {
					t.Errorf("%s: exit %d, want 1 with \"remote 'nosuch' not found\"\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, stdout, stderr)
				}
			}
			stdout, stderr, code := runAgentDeck(t, home, "remote", "sessions", "nosuch", "--json-envelope")
			if code != 1 || !strings.Contains(stdout, `"name": "nosuch"`) || !strings.Contains(stdout, `"error": "remote not found"`) {
				t.Errorf("--json-envelope: exit %d, want 1 with the error entry\nstdout: %s\nstderr: %s", code, stdout, stderr)
			}
		})
	}
	// The sibling: `remote update <name>` with nothing configured.
	stdout, stderr, code := runAgentDeck(t, t.TempDir(), "remote", "update", "nosuch", "--json")
	if code != 1 || !strings.Contains(stderr, "remote 'nosuch' not found") || strings.TrimSpace(stdout) == "[]" {
		t.Errorf("remote update nosuch --json with no remotes: exit %d %q %q, want 1 with \"remote 'nosuch' not found\"", code, stdout, stderr)
	}
	// Without a name, no remotes is still an empty answer, not an error.
	stdout, stderr, code = runAgentDeck(t, t.TempDir(), "remote", "sessions", "--json")
	if code != 0 || strings.TrimSpace(stdout) != "[]" {
		t.Errorf("remote sessions --json with no remotes: exit %d %q %q, want [] and 0", code, stdout, stderr)
	}
}
