package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The watcher subcommands that touch no profile state (help, routes) do not
// resolve the profile, so a CLAUDE_CONFIG_DIR naming a profile that does not
// exist does not print the #1790 fallback warning for them (follow-up to
// #2740 / #2746).
func TestWatcherStatelessCommandsSkipProfileResolution(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("AGENTDECK_PROFILE", "")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude-ghost"))

	dir, err := session.WatcherDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "clients.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{{"help"}, {"routes"}} {
		stderr := captureStderr(t, func() { handleWatcher("", args) })
		if strings.Contains(stderr, "CLAUDE_CONFIG_DIR") {
			t.Errorf("watcher %s printed the profile fallback warning: %q", args[0], stderr)
		}
	}

	// A command that reads the profile's state still resolves it and warns.
	stderr := captureStderr(t, func() { handleWatcher("", []string{"list"}) })
	if !strings.Contains(stderr, "CLAUDE_CONFIG_DIR") {
		t.Errorf("watcher list did not resolve the profile (no fallback warning): %q", stderr)
	}
}
