package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for `agent-deck group show [--resolved]` — the verification command
// for hand-edited [groups.X.claude] stanzas. The failure mode it exists to
// catch: a freshly appended stanza that silently fails to apply at launch
// (key typo, TOML parse error, missing env_file) with zero diagnostics.

func writeTestConfig(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".agent-deck")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func TestGroupShow_BasicAndNotFound(t *testing.T) {
	home := t.TempDir()

	stdout, _, code := runAgentDeck(t, home, "group", "create", "work")
	if code != 0 {
		t.Fatalf("group create failed: %s", stdout)
	}

	stdout, _, code = runAgentDeck(t, home, "group", "show", "work")
	if code != 0 {
		t.Fatalf("group show failed (exit %d): %s", code, stdout)
	}
	if !strings.Contains(stdout, "Group: work") {
		t.Errorf("expected group header, got:\n%s", stdout)
	}

	_, stderr, code := runAgentDeck(t, home, "group", "show", "nope")
	if code != 2 {
		t.Errorf("unknown group must exit 2, got %d (stderr: %s)", code, stderr)
	}
}

func TestGroupShow_ResolvedJSON(t *testing.T) {
	home := t.TempDir()
	writeTestConfig(t, home, `
[groups."work".claude]
env_file = "~/.agent-deck/groups/work.env"
command = "claude-work"
model = "claude-sonnet-4-6"
env = { AGENT_ROLE = "work" }
skills = ["store/loom"]
mcps = ["memory"]
`)

	if _, _, code := runAgentDeck(t, home, "group", "create", "work"); code != 0 {
		t.Fatal("group create failed")
	}

	stdout, stderr, code := runAgentDeck(t, home, "group", "show", "work", "--resolved", "--json")
	if code != 0 {
		t.Fatalf("group show --resolved --json failed (exit %d): %s / %s", code, stdout, stderr)
	}

	var payload struct {
		Success bool   `json:"success"`
		Path    string `json:"path"`
		Claude  struct {
			EnvFile       string            `json:"env_file"`
			EnvFileSource string            `json:"env_file_source"`
			EnvFileExists bool              `json:"env_file_exists"`
			Command       string            `json:"command"`
			CommandSource string            `json:"command_source"`
			Model         string            `json:"model"`
			Env           map[string]string `json:"env"`
			Skills        []string          `json:"skills"`
			MCPs          []string          `json:"mcps"`
			ConfigError   string            `json:"config_error"`
		} `json:"claude"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout)
	}

	c := payload.Claude
	if c.EnvFileSource != "group:work" || c.EnvFileExists {
		t.Errorf("env_file source=%q exists=%v want group:work / false", c.EnvFileSource, c.EnvFileExists)
	}
	if c.Command != "claude-work" || c.CommandSource != "group:work" {
		t.Errorf("command=%q [%s] want claude-work [group:work]", c.Command, c.CommandSource)
	}
	if c.Model != "claude-sonnet-4-6" {
		t.Errorf("model=%q want claude-sonnet-4-6", c.Model)
	}
	if c.Env["AGENT_ROLE"] != "work" {
		t.Errorf("env=%v want AGENT_ROLE=work", c.Env)
	}
	if len(c.Skills) != 1 || c.Skills[0] != "store/loom" {
		t.Errorf("skills=%v want [store/loom]", c.Skills)
	}
	if len(c.MCPs) != 1 || c.MCPs[0] != "memory" {
		t.Errorf("mcps=%v want [memory]", c.MCPs)
	}
	if c.ConfigError != "" {
		t.Errorf("unexpected config_error: %s", c.ConfigError)
	}

	// env_file_exists flips once the file lands.
	envPath := filepath.Join(home, ".agent-deck", "groups", "work.env")
	if err := os.MkdirAll(filepath.Dir(envPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(envPath, []byte("export A=1\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	stdout, _, code = runAgentDeck(t, home, "group", "show", "work", "--resolved", "--json")
	if code != 0 {
		t.Fatalf("second show failed: %s", stdout)
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !payload.Claude.EnvFileExists {
		t.Error("env_file_exists must be true after the file is created")
	}
}

func TestGroupShow_ResolvedSurfacesBrokenConfig(t *testing.T) {
	home := t.TempDir()

	if _, _, code := runAgentDeck(t, home, "group", "create", "work"); code != 0 {
		t.Fatal("group create failed")
	}

	// Break the config AFTER group creation (the live failure shape: a
	// hand-appended stanza with a syntax error).
	writeTestConfig(t, home, `
[groups."work".claude
env_file = "broken
`)

	stdout, _, code := runAgentDeck(t, home, "group", "show", "work", "--resolved")
	if code != 0 {
		t.Fatalf("group show must still succeed on a broken config (exit %d): %s", code, stdout)
	}
	if !strings.Contains(stdout, "config.toml ERROR") {
		t.Errorf("broken config.toml must be loudly surfaced:\n%s", stdout)
	}
}
