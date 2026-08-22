package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIssue2045LaunchHelpOffersNamedAccountSlot(t *testing.T) {
	home := t.TempDir()
	stdout, stderr, code := runAgentDeck(t, home, "launch", "--help")
	if code != 0 {
		t.Fatalf("launch --help exit = %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout+stderr, "-account") {
		t.Fatalf("launch --help does not offer --account:\n%s%s", stdout, stderr)
	}
}

func TestIssue2045AccountsListsConfiguredSlots(t *testing.T) {
	home := t.TempDir()
	configDir := filepath.Join(home, ".config", "agent-deck")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := `[profiles.personal.claude]
config_dir = "~/.claude-personal"

[profiles.work.claude]
config_dir = "~/.claude-work"
`
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runAgentDeck(t, home, "accounts", "--json")
	if code != 0 {
		t.Fatalf("accounts --json exit = %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var got []struct {
		Name      string `json:"name"`
		ConfigDir string `json:"config_dir"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("accounts --json returned invalid JSON: %v\n%s", err, stdout)
	}
	if len(got) != 2 || got[0].Name != "personal" || got[1].Name != "work" {
		t.Fatalf("accounts = %#v, want personal and work sorted by name", got)
	}
	if got[0].ConfigDir != filepath.Join(home, ".claude-personal") || got[1].ConfigDir != filepath.Join(home, ".claude-work") {
		t.Fatalf("accounts did not return resolved config dirs: %#v", got)
	}
}
