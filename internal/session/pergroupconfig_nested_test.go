package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withIsolatedHomeAndConfig sets HOME to a temp dir, writes config.toml with
// the supplied body, unsets CLAUDE_CONFIG_DIR + AGENTDECK_PROFILE, and clears
// the user-config cache. It restores everything on test cleanup. Returns the
// temp HOME path so tests can build expected absolute paths.
//
// Distinct from the lighter-weight `withTempHomeAndConfig` in
// userconfig_web_test.go because the per-group config tests need
// CLAUDE_CONFIG_DIR / AGENTDECK_PROFILE explicitly unset to keep the
// resolver result deterministic.
func withIsolatedHomeAndConfig(t *testing.T, configBody string) string {
	t.Helper()
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	origProfile := os.Getenv("AGENTDECK_PROFILE")
	origClaudeDir := os.Getenv("CLAUDE_CONFIG_DIR")
	t.Cleanup(func() {
		_ = os.Setenv("HOME", origHome)
		if origProfile != "" {
			_ = os.Setenv("AGENTDECK_PROFILE", origProfile)
		} else {
			_ = os.Unsetenv("AGENTDECK_PROFILE")
		}
		if origClaudeDir != "" {
			_ = os.Setenv("CLAUDE_CONFIG_DIR", origClaudeDir)
		} else {
			_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
		}
		ClearUserConfigCache()
	})

	_ = os.Setenv("HOME", tmpHome)
	_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
	_ = os.Unsetenv("AGENTDECK_PROFILE")

	agentDeckDir := filepath.Join(tmpHome, ".agent-deck")
	if err := os.MkdirAll(agentDeckDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentDeckDir, "config.toml"), []byte(configBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()
	return tmpHome
}

// TestPerGroupConfig_ChildGroupInheritsParentConfigDir locks the parent-group
// inheritance contract: a session in a child group whose exact path has no
// [groups."<path>".claude] block must inherit the nearest ancestor's
// config_dir.
//
// Without inheritance the child path resolves to the global/default claude
// config, breaking per-group account isolation on macOS where Claude keys
// OAuth credentials by the literal CLAUDE_CONFIG_DIR path.
func TestPerGroupConfig_ChildGroupInheritsParentConfigDir(t *testing.T) {
	tmpHome := withIsolatedHomeAndConfig(t, `
[groups."personal".claude]
config_dir = "~/.claude-personal"
`)

	wantDir := filepath.Join(tmpHome, ".claude-personal")

	cases := []struct {
		name      string
		groupPath string
	}{
		{"exact parent", "personal"},
		{"direct child", "personal/work"},
		{"nested grandchild", "personal/work/projects"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, source := GetClaudeConfigDirSourceForGroup(tc.groupPath)
			if source != "group" {
				t.Errorf("source=%q want %q", source, "group")
			}
			if path != wantDir {
				t.Errorf("path=%q want %q", path, wantDir)
			}
		})
	}
}

// TestPerGroupConfig_ChildGroupPrefersNearestAncestor: when the chain has
// overrides at multiple levels, the nearest ancestor wins.
func TestPerGroupConfig_ChildGroupPrefersNearestAncestor(t *testing.T) {
	tmpHome := withIsolatedHomeAndConfig(t, `
[groups."personal".claude]
config_dir = "~/.claude-personal"

[groups."personal/work".claude]
config_dir = "~/.claude-personal-work"
`)

	wantWork := filepath.Join(tmpHome, ".claude-personal-work")
	wantRoot := filepath.Join(tmpHome, ".claude-personal")

	if got, _ := GetClaudeConfigDirSourceForGroup("personal/work/projects"); got != wantWork {
		t.Errorf("grandchild path=%q want %q", got, wantWork)
	}
	if got, _ := GetClaudeConfigDirSourceForGroup("personal/work"); got != wantWork {
		t.Errorf("direct child path=%q want %q", got, wantWork)
	}
	if got, _ := GetClaudeConfigDirSourceForGroup("personal/other"); got != wantRoot {
		t.Errorf("sibling child path=%q want %q", got, wantRoot)
	}
}

// TestPerGroupConfig_ChildGroupFallsThroughToProfileWhenNoAncestor: when
// no ancestor has a config_dir, the resolver must continue down the chain
// (profile → global → default) — inheritance doesn't introduce a phantom
// "group" source.
func TestPerGroupConfig_ChildGroupFallsThroughToProfileWhenNoAncestor(t *testing.T) {
	tmpHome := withIsolatedHomeAndConfig(t, `
[profiles.p.claude]
config_dir = "~/.claude-profile"
`)
	_ = os.Setenv("AGENTDECK_PROFILE", "p")
	ClearUserConfigCache()

	path, source := GetClaudeConfigDirSourceForGroup("foo/bar")
	if source != "profile" {
		t.Errorf("source=%q want %q", source, "profile")
	}
	wantDir := filepath.Join(tmpHome, ".claude-profile")
	if path != wantDir {
		t.Errorf("path=%q want %q", path, wantDir)
	}
}

// TestPerGroupConfig_ChildGroupInstanceInheritsParentConfigDir locks the
// instance-chain (spawn path) variant: a session whose GroupPath is a child
// must export the parent's config_dir in its spawn command.
func TestPerGroupConfig_ChildGroupInstanceInheritsParentConfigDir(t *testing.T) {
	tmpHome := withIsolatedHomeAndConfig(t, `
[groups."personal".claude]
config_dir = "~/.claude-personal"
`)

	inst := NewInstanceWithGroupAndTool("s1", "/tmp/p", "personal/work", "claude")
	cmd := inst.buildClaudeCommand("claude")

	wantDir := filepath.Join(tmpHome, ".claude-personal")
	if !strings.Contains(cmd, "CLAUDE_CONFIG_DIR="+wantDir) {
		t.Errorf("child-group spawn missing inherited CLAUDE_CONFIG_DIR=%s\ngot: %s", wantDir, cmd)
	}
}

// TestPerGroupConfig_ChildGroupEnvFileInheritance mirrors the config_dir
// inheritance for env_file so nested groups don't silently drop the
// parent's env_file.
func TestPerGroupConfig_ChildGroupEnvFileInheritance(t *testing.T) {
	_ = withIsolatedHomeAndConfig(t, `
[groups."personal".claude]
env_file = "~/.agent-deck/personal.env"

[groups."personal/scoped".claude]
env_file = "~/.agent-deck/scoped.env"
`)

	cfg, err := LoadUserConfig()
	if err != nil || cfg == nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}

	if got := cfg.GetGroupClaudeEnvFile("personal"); got != "~/.agent-deck/personal.env" {
		t.Errorf("exact parent env_file=%q want personal.env", got)
	}
	if got := cfg.GetGroupClaudeEnvFile("personal/other"); got != "~/.agent-deck/personal.env" {
		t.Errorf("child inherits parent env_file=%q want personal.env", got)
	}
	if got := cfg.GetGroupClaudeEnvFile("personal/scoped"); got != "~/.agent-deck/scoped.env" {
		t.Errorf("nearest-ancestor wins for env_file=%q want scoped.env", got)
	}
	if got := cfg.GetGroupClaudeEnvFile("personal/scoped/leaf"); got != "~/.agent-deck/scoped.env" {
		t.Errorf("grandchild inherits scoped env_file=%q want scoped.env", got)
	}
	if got := cfg.GetGroupClaudeEnvFile("unrelated/leaf"); got != "" {
		t.Errorf("unrelated tree should have no env_file inheritance, got %q", got)
	}
}
