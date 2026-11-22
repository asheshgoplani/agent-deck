package session

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A New Session on the Default account, in a group with no account slot,
// resolves to the host's own ~/.claude. When that dir has no login, Claude
// opens its first-run screen. The resolver must fall back to a configured
// account, or refuse with a message naming the fix.
func TestResolveNewSessionAccount_UnauthenticatedDefaultConfigDir(t *testing.T) {
	skipIfLoginAssumed(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("AGENTDECK_PROFILE", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	// deckbox ~/.claude exists but is not logged in.
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	workDir := filepath.Join(home, "acct-work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &UserConfig{Profiles: map[string]ProfileSettings{
		"work": {Claude: ProfileClaudeSettings{ConfigDir: workDir}},
	}}

	t.Run("refuses when nothing configured is logged in", func(t *testing.T) {
		acct, err := ResolveNewSessionAccount(cfg, "claude", "projects", nil)
		if err == nil {
			t.Fatalf("want refusal, got account %q and no error", acct)
		}
		if !strings.Contains(err.Error(), "agent-deck accounts") {
			t.Fatalf("message must name the fix, got: %v", err)
		}
	})

	t.Run("refuses with no accounts at all", func(t *testing.T) {
		if _, err := ResolveNewSessionAccount(&UserConfig{}, "claude", "projects", nil); err == nil {
			t.Fatal("want refusal with no accounts configured")
		}
	})

	if err := os.WriteFile(filepath.Join(workDir, ".credentials.json"), []byte(`{"claudeAiOauth":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("falls back to the logged-in configured account", func(t *testing.T) {
		acct, err := ResolveNewSessionAccount(cfg, "claude", "projects", nil)
		if err != nil || acct != "work" {
			t.Fatalf("want fallback to work, got %q, %v", acct, err)
		}
	})

	if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(`{"claudeAiOauth":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("logged-in default dir stays on inherit", func(t *testing.T) {
		acct, err := ResolveNewSessionAccount(cfg, "claude", "projects", nil)
		if err != nil || acct != "" {
			t.Fatalf("want inherit, got %q, %v", acct, err)
		}
	})
}

func TestClaudeConfigDirHasLogin(t *testing.T) {
	skipIfLoginAssumed(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	dir := t.TempDir()
	if ClaudeConfigDirHasLogin(dir) {
		t.Fatal("empty dir must not count as logged in")
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"a@b"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !ClaudeConfigDirHasLogin(dir) {
		t.Fatal("oauthAccount in .claude.json must count as logged in")
	}
}

// skipIfLoginAssumed skips the filesystem-login tests where the probe assumes
// a login (macOS keychain cannot be probed).
func skipIfLoginAssumed(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("login probe assumes a keychain login on macOS")
	}
}
