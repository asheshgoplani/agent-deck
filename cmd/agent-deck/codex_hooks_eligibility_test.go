package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestCodexHooksEligibilityChecksOwnershipFirst(t *testing.T) {
	for _, config := range []string{"[shell\n", "[shell]\ninit_script = 'export CODEX_HOME=/elsewhere'\n"} {
		t.Run(config, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			t.Setenv("CODEX_HOME", "")
			dir := filepath.Join(home, ".config", "agent-deck")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			session.ClearUserConfigCache()
			t.Cleanup(session.ClearUserConfigCache)
			for _, kind := range []string{"nil", "other", "SSH", "sandbox", "local"} {
				t.Run(kind, func(t *testing.T) {
					var inst *session.Instance
					if kind != "nil" {
						inst = &session.Instance{Tool: "codex", Command: "codex", ProjectPath: home}
					}
					switch kind {
					case "other":
						inst.Tool = "shell"
					case "SSH":
						inst.SSHHost = "example.invalid"
					case "sandbox":
						inst.Sandbox = session.NewSandboxConfig("example/sandbox")
					}
					path, reason, err := resolveCodexHooksEligibility(inst)
					if kind == "local" {
						if path != "" || (err == nil && !strings.Contains(reason, "shell startup")) {
							t.Fatalf("expected invalid or ambiguous local config: %q %q %v", path, reason, err)
						}
						return
					}
					if path != "" || err != nil || reason == "" {
						t.Fatalf("wrong eligibility precedence: %q %q %v", path, reason, err)
					}
					if kind == "other" && !strings.Contains(reason, "does not use Codex") {
						t.Fatal(reason)
					}
					if kind == "SSH" && !strings.Contains(reason, "remote host") {
						t.Fatal(reason)
					}
					if kind == "sandbox" && !strings.Contains(reason, "sandbox") {
						t.Fatal(reason)
					}
				})
			}
		})
	}
}

func TestCodexHooksShellEnvironmentCases(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*session.UserConfig)
		want bool
	}{
		{"none", func(*session.UserConfig) {}, false},
		{"global env files", func(c *session.UserConfig) { c.Shell.EnvFiles = []string{".env"} }, true},
		{"init script", func(c *session.UserConfig) { c.Shell.InitScript = "direnv export bash" }, true},
		{"launch shell", func(c *session.UserConfig) { v := true; c.Shell.LaunchShell = &v }, true},
		{"disabled launch shell", func(c *session.UserConfig) { v := false; c.Shell.LaunchShell = &v }, false},
		{"Codex env file", func(c *session.UserConfig) { c.Codex.EnvFile = "codex.env" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &session.UserConfig{}
			tc.set(cfg)
			if got := codexHooksHasShellEnvironment(cfg); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
