package session

import (
	"os"
	"path/filepath"
	"testing"
)

// The login check must fail safe toward "logged in": a new session on the
// implicit ~/.claude keeps today's behaviour (no refusal, no redirect to
// another account) unless every known auth signal is absent.
func TestResolveNewSessionAccount_FailsSafeTowardLoggedIn(t *testing.T) {
	skipIfLoginAssumed(t)

	write := func(t *testing.T, path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name  string
		env   map[string]string
		files map[string]string // relative to HOME
		cfg   func(workDir string) *UserConfig
	}{
		{name: "bedrock", env: map[string]string{"CLAUDE_CODE_USE_BEDROCK": "1"}},
		{name: "vertex", env: map[string]string{"CLAUDE_CODE_USE_VERTEX": "1"}},
		{name: "auth token", env: map[string]string{"ANTHROPIC_AUTH_TOKEN": "x"}},
		{name: "apiKeyHelper in settings.json", files: map[string]string{".claude/settings.json": `{"apiKeyHelper":"/bin/true"}`}},
		{name: "primaryApiKey in .claude.json", files: map[string]string{".claude.json": `{"primaryApiKey":"x"}`}},
		{name: "apiKeyHelper in .claude.json", files: map[string]string{".claude.json": `{"apiKeyHelper":"/bin/true"}`}},
		{name: "malformed .claude.json", files: map[string]string{".claude.json": `{not json`}},
		{name: "unreadable .claude.json", files: map[string]string{".claude.json/x": ``}},
		{name: "malformed settings.json", files: map[string]string{".claude/settings.json": `{not json`}},
		{name: "command alias", cfg: func(string) *UserConfig { return &UserConfig{Claude: ClaudeSettings{Command: "cdw"}} }},
		{name: "claude env_file", cfg: func(string) *UserConfig { return &UserConfig{Claude: ClaudeSettings{EnvFile: "~/.envrc"}} }},
		{name: "shell env_files", cfg: func(string) *UserConfig {
			return &UserConfig{Shell: ShellSettings{EnvFiles: []string{"~/.env"}}}
		}},
		{name: "group env_file", cfg: func(string) *UserConfig {
			return &UserConfig{Groups: map[string]GroupSettings{"projects": {Claude: GroupClaudeSettings{EnvFile: "~/.envrc"}}}}
		}},
		{name: "group command", cfg: func(string) *UserConfig {
			return &UserConfig{Groups: map[string]GroupSettings{"projects": {Claude: GroupClaudeSettings{Command: "claude-vertex"}}}}
		}},
		{name: "conductor env_file", cfg: func(string) *UserConfig {
			return &UserConfig{Conductors: map[string]ConductorOverrides{"c": {Claude: ConductorClaudeSettings{EnvFile: "~/.envrc"}}}}
		}},
	}

	for _, tc := range cases {
		for _, withAccount := range []bool{false, true} {
			name := tc.name
			if withAccount {
				name += " with a logged-in account configured"
			}
			t.Run(name, func(t *testing.T) {
				home := t.TempDir()
				t.Setenv("HOME", home)
				for _, k := range []string{"CLAUDE_CONFIG_DIR", "AGENTDECK_PROFILE", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN",
					"ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
					t.Setenv(k, "")
				}
				for k, v := range tc.env {
					t.Setenv(k, v)
				}
				ClearUserConfigCache()
				t.Cleanup(ClearUserConfigCache)
				if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
					t.Fatal(err)
				}
				for rel, body := range tc.files {
					write(t, filepath.Join(home, rel), body)
				}
				workDir := filepath.Join(home, "acct-work")
				var cfg *UserConfig
				if tc.cfg != nil {
					cfg = tc.cfg(workDir)
				} else {
					cfg = &UserConfig{}
				}
				if withAccount {
					write(t, filepath.Join(workDir, ".credentials.json"), `{"claudeAiOauth":{}}`)
					if cfg.Profiles == nil {
						cfg.Profiles = map[string]ProfileSettings{}
					}
					cfg.Profiles["work"] = ProfileSettings{Claude: ProfileClaudeSettings{ConfigDir: workDir}}
				}
				acct, err := ResolveNewSessionAccount(cfg, "claude", "projects", nil)
				if err != nil || acct != "" {
					t.Fatalf("want today's behaviour (no account, no error), got %q, %v", acct, err)
				}
			})
		}
	}
}

func TestClaudeConfigDirHasLogin_PresenceOnly(t *testing.T) {
	skipIfLoginAssumed(t)
	for _, k := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX"} {
		t.Setenv(k, "")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"apiKeyHelper":"/bin/true"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !ClaudeConfigDirHasLogin(dir) {
		t.Fatal("apiKeyHelper in settings.json must count as logged in")
	}
}

// A Claude-compatible custom tool (e.g. [tools.glm] with command = "claude")
// brings its own env and auth, so it is never refused or redirected.
func TestResolveNewSessionAccount_OnlyPlainClaudeTool(t *testing.T) {
	skipIfLoginAssumed(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range []string{"CLAUDE_CONFIG_DIR", "AGENTDECK_PROFILE", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX"} {
		t.Setenv(k, "")
	}
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveNewSessionAccount(&UserConfig{}, "claude", "projects", nil); err == nil {
		t.Fatal("plain claude on a bare host must still refuse")
	}
	for _, tool := range []string{"glm", "shell", "codex", ""} {
		if acct, err := ResolveNewSessionAccount(&UserConfig{}, tool, "projects", nil); err != nil || acct != "" {
			t.Fatalf("tool %q: want no change, got %q, %v", tool, acct, err)
		}
	}
}
