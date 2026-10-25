package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// #2895: `add` and `launch` without --account start a Claude session on
// [claude].default_account, the same default the New Session dialog
// preselects. An explicit --account or AGENTDECK_ACCOUNT still wins, a group
// config_dir still wins, and non-Claude tools never get a slot.
func TestAddAndLaunchHonourDefaultClaudeAccount(t *testing.T) {
	home := t.TempDir()
	writeTestConfig(t, home, `[claude]
default_account = "work"

[profiles.ai.claude]
config_dir = "~/.claude-ai"

[profiles.work.claude]
config_dir = "~/.claude-work"

[groups."pinned".claude]
config_dir = "~/.claude-pinned"
`)
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}

	add := func(t *testing.T, env []string, args ...string) string {
		t.Helper()
		args = append([]string{"add", project, "--no-parent", "--json"}, args...)
		stdout, stderr, code := runAgentDeckEnv(t, home, "", env, args...)
		if code != 0 {
			t.Fatalf("add %v exit %d\nstdout: %s\nstderr: %s", args, code, stdout, stderr)
		}
		var row struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(stdout), &row); err != nil || row.ID == "" {
			t.Fatalf("parse add output: %v\n%s", err, stdout)
		}
		return row.ID
	}
	accountOf := func(t *testing.T, id string) string {
		t.Helper()
		stdout, stderr, code := runAgentDeck(t, home, "list", "--json")
		if code != 0 {
			t.Fatalf("list exit %d: %s", code, stderr)
		}
		var rows []struct {
			ID      string `json:"id"`
			Account string `json:"account"`
		}
		if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
			t.Fatalf("parse list: %v\n%s", err, stdout)
		}
		for _, r := range rows {
			if r.ID == id {
				return r.Account
			}
		}
		t.Fatalf("session %s not listed:\n%s", id, stdout)
		return ""
	}

	cases := []struct {
		name string
		env  []string
		args []string
		want string
	}{
		{name: "claude without --account gets the default", args: []string{"-t", "plain", "-c", "claude"}, want: "work"},
		{name: "explicit --account wins", args: []string{"-t", "explicit", "-c", "claude", "--account", "ai"}, want: "ai"},
		{name: "AGENTDECK_ACCOUNT wins", env: []string{"AGENTDECK_ACCOUNT=ai"}, args: []string{"-t", "env", "-c", "claude"}, want: "ai"},
		{name: "group config_dir wins", args: []string{"-t", "grouped", "-c", "claude", "-g", "pinned"}, want: ""},
		{name: "non-Claude tool gets no slot", args: []string{"-t", "shell", "-c", "shell"}, want: ""},
		// An agent-deck Claude session exports its account's
		// CLAUDE_CONFIG_DIR; a child it creates keeps that account, as it
		// did before the default existed.
		{name: "ambient CLAUDE_CONFIG_DIR wins", env: []string{"CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude-ai")}, args: []string{"-t", "ambient", "-c", "claude"}, want: ""},
		// The slot names a config_dir on this machine, not on the remote.
		{name: "ssh target gets no slot", args: []string{"-t", "remote", "-c", "claude", "--ssh", "alice@host-a", "--remote-path", "/srv/proj"}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := accountOf(t, add(t, tc.env, tc.args...)); got != tc.want {
				t.Fatalf("stored account = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("launch without --account gets the default", func(t *testing.T) {
		// launch needs tmux; a no-op tmux lets it reach create/save without
		// touching any real tmux server (as TestIssue2045LaunchPersistsNamedAccountSlot).
		binDir := t.TempDir()
		fakeTmux := "#!/bin/sh\ncase \" $* \" in\n  *' has-session '*) exit 1 ;;\nesac\nexit 0\n"
		if err := os.WriteFile(filepath.Join(binDir, "tmux"), []byte(fakeTmux), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

		stdout, stderr, code := runAgentDeck(t, home, "launch", project, "-t", "launched", "-c", "claude", "--no-parent", "--no-wait", "--json")
		if code != 0 {
			t.Fatalf("launch exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
		}
		var row struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(stdout), &row); err != nil || row.ID == "" {
			t.Fatalf("parse launch output: %v\n%s", err, stdout)
		}
		if got := accountOf(t, row.ID); got != "work" {
			t.Fatalf("launched account = %q, want the default \"work\"", got)
		}
	})
}
