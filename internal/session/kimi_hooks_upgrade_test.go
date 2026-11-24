package session_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestKimiPreToolHookUsesSyncMarkerAndUpgradesInPlace(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	installed, err := session.InjectKimiHooks(dir)
	if err != nil || !installed {
		t.Fatalf("InjectKimiHooks = %v, %v", installed, err)
	}
	assertKimiHookCommands(t, dir)

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	plain := strings.Replace(string(data), "AGENTDECK_PRETOOL_SYNC=1 agent-deck hook-handler", "agent-deck hook-handler", 1)
	if plain == string(data) {
		t.Fatal("installed config has no synchronous PreToolUse command to downgrade")
	}
	if err := os.WriteFile(configPath, []byte(plain), 0o600); err != nil {
		t.Fatal(err)
	}
	if session.CheckKimiHooksInstalled(dir) {
		t.Fatal("legacy plain PreToolUse hook reported as current")
	}
	installed, err = session.InjectKimiHooks(dir)
	if err != nil || !installed {
		t.Fatalf("upgrade InjectKimiHooks = %v, %v", installed, err)
	}
	assertKimiHookCommands(t, dir)
	installed, err = session.InjectKimiHooks(dir)
	if err != nil || installed {
		t.Fatalf("idempotent InjectKimiHooks = %v, %v", installed, err)
	}
}

func assertKimiHookCommands(t *testing.T, dir string) {
	t.Helper()
	entries := readKimiHookEntries(t, dir)
	if len(entries) != len(session.KimiHookEventsForInstall()) {
		t.Fatalf("hook count = %d, want %d", len(entries), len(session.KimiHookEventsForInstall()))
	}
	preToolCount := 0
	for _, entry := range entries {
		want := "agent-deck hook-handler"
		if entry.Event == "PreToolUse" {
			want = "AGENTDECK_PRETOOL_SYNC=1 agent-deck hook-handler"
			preToolCount++
		}
		if entry.Command != want {
			t.Errorf("%s command = %q, want %q", entry.Event, entry.Command, want)
		}
	}
	if preToolCount != 1 {
		t.Fatalf("PreToolUse owned entries = %d, want exactly one", preToolCount)
	}
}
