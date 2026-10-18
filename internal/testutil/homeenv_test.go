package testutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIsolateHomeClearsToolConfigDirs pins that IsolateHome does not leave a
// way back to the developer's live tool config. Each tool config-dir variable
// beats $HOME in agent-deck's resolvers, and agent-deck exports
// CLAUDE_CONFIG_DIR into the sessions it starts, so a test run started from a
// worker session used to inherit the real ~/.claude (or ~/.codex, ...) even
// with HOME swapped. After IsolateHome every one of them must be unset, and
// cleanup must put the caller's values back.
func TestIsolateHomeClearsToolConfigDirs(t *testing.T) {
	live := t.TempDir() // stands in for the developer's live config dirs
	want := map[string]string{}
	for _, k := range ToolConfigDirEnvs {
		want[k] = filepath.Join(live, strings.ToLower(k))
		t.Setenv(k, want[k])
	}

	cleanup := IsolateHome()
	home := os.Getenv("HOME")
	for _, k := range ToolConfigDirEnvs {
		if v, ok := os.LookupEnv(k); ok {
			cleanup()
			t.Fatalf("after IsolateHome %s=%q is still set; agent-deck resolves it ahead of the isolated HOME %q, so tests would write into the live dir", k, v, home)
		}
	}
	if home == "" || strings.HasPrefix(home, live) {
		cleanup()
		t.Fatalf("IsolateHome set HOME=%q, want a fresh temp dir", home)
	}
	cleanup()

	for _, k := range ToolConfigDirEnvs {
		if got := os.Getenv(k); got != want[k] {
			t.Errorf("cleanup did not restore %s: got %q, want %q", k, got, want[k])
		}
	}
}
