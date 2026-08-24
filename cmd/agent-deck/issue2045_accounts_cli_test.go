package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestIssue2045LaunchPersistsNamedAccountSlot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("AGENTDECK_PROFILE", "ch_support_test")
	configDir := filepath.Join(home, ".config", "agent-deck")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(`[profiles.work.claude]
config_dir = "~/.claude-work"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	// The launch path requires tmux, but persistence is the behavior under
	// test. A successful no-op executable lets the real command reach every
	// create/start/save stage without sharing the developer's tmux server.
	binDir := t.TempDir()
	fakeTmux := "#!/bin/sh\ncase \" $* \" in\n  *' has-session '*) exit 1 ;;\nesac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "tmux"), []byte(fakeTmux), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stdout, stderr, code := runAgentDeck(t, home, "launch", home, "-c", "shell", "--account", "work", "--no-wait")
	if code != 0 {
		t.Fatalf("launch exit = %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	storage, err := session.NewStorageWithProfile("ch_support_test")
	if err != nil {
		t.Fatalf("open launch storage: %v", err)
	}
	sessions, err := storage.Load()
	if err != nil {
		t.Fatalf("load launched session: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("persisted sessions = %d, want 1", len(sessions))
	}
	if sessions[0].Account != "work" {
		t.Fatalf("persisted launch account = %q, want work", sessions[0].Account)
	}

	t.Run("RemoteSession", func(t *testing.T) {
		t.Skip("RemoteSession out of scope: launch creates a local session and accepts no remote target")
	})
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
