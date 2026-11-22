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
		{name: "settings.json env bedrock", files: map[string]string{".claude/settings.json": `{"env":{"CLAUDE_CODE_USE_BEDROCK":"1"}}`}},
		{name: "settings.json env api key", files: map[string]string{".claude/settings.json": `{"env":{"ANTHROPIC_API_KEY":"sk-x"}}`}},
		{name: "settings.local.json env", files: map[string]string{".claude/settings.local.json": `{"env":{"ANTHROPIC_BASE_URL":"http://x"}}`}},
		{name: "settings.local.json apiKeyHelper", files: map[string]string{".claude/settings.local.json": `{"apiKeyHelper":"/bin/true"}`}},
		{name: "managed settings env", files: map[string]string{"managed/managed-settings.json": `{"env":{"CLAUDE_CODE_USE_VERTEX":"1"}}`}},
		{name: "managed settings apiKeyHelper", files: map[string]string{"managed/managed-settings.json": `{"apiKeyHelper":"/bin/true"}`}},
		{name: "onboarding completed, no other signal", files: map[string]string{".claude.json": `{"hasCompletedOnboarding":true}`}},
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
				managed := filepath.Join(home, "managed", "managed-settings.json")
				prev := claudeManagedSettingsPath
				claudeManagedSettingsPath = managed
				t.Cleanup(func() { claudeManagedSettingsPath = prev })
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

// The refusal and the fallback fire only for a really fresh dir: onboarding
// not completed and no auth signal anywhere.
func TestClaudeConfigDirHasLogin_FreshDirNeedsNoOnboardingFlag(t *testing.T) {
	skipIfLoginAssumed(t)
	for _, k := range claudeAuthEnvSignals {
		t.Setenv(k, "")
	}
	prev := claudeManagedSettingsPath
	claudeManagedSettingsPath = filepath.Join(t.TempDir(), "none.json")
	t.Cleanup(func() { claudeManagedSettingsPath = prev })
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{"numStartups":3,"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if ClaudeConfigDirHasLogin(dir) {
		t.Fatal("a dir with config but no completed onboarding and no auth is a first run")
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{"hasCompletedOnboarding":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !ClaudeConfigDirHasLogin(dir) {
		t.Fatal("completed onboarding means no first-run screen")
	}
}

// A fallback TARGET needs a positive login. A configured dir whose auth state
// is merely unknown (unreadable or malformed file, a bare env block) is not
// moved onto; the refusal stands.
func TestResolveNewSessionAccount_FallbackTargetNeedsPositiveLogin(t *testing.T) {
	skipIfLoginAssumed(t)
	for name, files := range map[string]map[string]string{
		"malformed .claude.json":    {".claude.json": `{not json`},
		"env block only":            {"settings.json": `{"env":{"FOO":"1"}}`},
		"onboarding flag only":      {".claude.json": `{"hasCompletedOnboarding":true}`},
		"empty credentials file":    {".credentials.json": ``},
		"unrelated .claude.json":    {".claude.json": `{"numStartups":2}`},
		"unreadable .claude.json/x": {".claude.json/x": ``},
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			for _, k := range claudeAuthEnvSignals {
				t.Setenv(k, "")
			}
			for _, k := range []string{"CLAUDE_CONFIG_DIR", "AGENTDECK_PROFILE"} {
				t.Setenv(k, "")
			}
			prev := claudeManagedSettingsPath
			claudeManagedSettingsPath = filepath.Join(home, "none.json")
			t.Cleanup(func() { claudeManagedSettingsPath = prev })
			ClearUserConfigCache()
			t.Cleanup(ClearUserConfigCache)
			if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
				t.Fatal(err)
			}
			workDir := filepath.Join(home, "acct-work")
			for rel, body := range files {
				p := filepath.Join(workDir, rel)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg := &UserConfig{Profiles: map[string]ProfileSettings{"work": {Claude: ProfileClaudeSettings{ConfigDir: workDir}}}}
			acct, err := ResolveNewSessionAccount(cfg, "claude", "projects", nil)
			if err == nil {
				t.Fatalf("want a refusal, got a move onto %q", acct)
			}
		})
	}
}
