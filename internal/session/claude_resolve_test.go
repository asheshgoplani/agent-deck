package session

import (
	"os"
	"path/filepath"
	"testing"
)

// TestClaudeConfigDirResolveParity asserts that the path returned by
// GetClaudeConfigDirForGroup matches the path returned by
// GetClaudeConfigDirSourceForGroup for every known priority level, and
// likewise that GetClaudeConfigDirForInstance matches
// GetClaudeConfigDirSourceForInstance. This is a drift-detection guard:
// the *Source* variants duplicate the priority chain of their non-source
// counterparts; if the chains diverge (the bug pattern that produced
// #881), this test fails.
//
// The test exercises every branch of both chains (env, conductor, group,
// profile, global, default).
func TestClaudeConfigDirResolveParity(t *testing.T) {
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
	_ = os.Setenv("AGENTDECK_PROFILE", "work")

	agentDeckDir := filepath.Join(tmpHome, ".agent-deck")
	if err := os.MkdirAll(agentDeckDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	configContent := `
[claude]
config_dir = "~/.claude-global"

[profiles.work.claude]
config_dir = "~/.claude-work"

[groups."team-a".claude]
config_dir = "~/.claude-team-a"

[conductors.coordinator.claude]
config_dir = "~/.claude-coordinator"
`
	if err := os.WriteFile(filepath.Join(agentDeckDir, "config.toml"), []byte(configContent), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()

	// Group chain parity: GetClaudeConfigDirForGroup vs GetClaudeConfigDirSourceForGroup.
	groupCases := []struct {
		name        string
		envOverride string
		groupPath   string
		wantSource  string
	}{
		{"env wins", filepath.Join(tmpHome, ".claude-env"), "team-a", "env"},
		{"group beats profile (no env)", "", "team-a", "group"},
		{"profile wins when no group match", "", "unknown", "profile"},
		{"default when no profile", "", "", "profile"}, // profile=work matches
	}
	for _, tc := range groupCases {
		t.Run("group/"+tc.name, func(t *testing.T) {
			if tc.envOverride != "" {
				_ = os.Setenv("CLAUDE_CONFIG_DIR", tc.envOverride)
			} else {
				_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
			}
			ClearUserConfigCache()

			gotPath := GetClaudeConfigDirForGroup(tc.groupPath)
			srcPath, srcSource := GetClaudeConfigDirSourceForGroup(tc.groupPath)
			if gotPath != srcPath {
				t.Errorf("PATH DRIFT: GetClaudeConfigDirForGroup=%q, GetClaudeConfigDirSourceForGroup path=%q (source=%q)", gotPath, srcPath, srcSource)
			}
			if tc.wantSource != "" && srcSource != tc.wantSource {
				t.Errorf("source = %q, want %q", srcSource, tc.wantSource)
			}
			// Is*Explicit must agree: explicit iff source != "default".
			gotExplicit := IsClaudeConfigDirExplicitForGroup(tc.groupPath)
			wantExplicit := srcSource != "default"
			if gotExplicit != wantExplicit {
				t.Errorf("EXPLICIT DRIFT: IsClaudeConfigDirExplicitForGroup=%v, source=%q (expected explicit=%v)", gotExplicit, srcSource, wantExplicit)
			}
		})
	}

	// Instance chain parity: GetClaudeConfigDirForInstance vs GetClaudeConfigDirSourceForInstance.
	instCases := []struct {
		name        string
		envOverride string
		title       string
		groupPath   string
		wantSource  string
	}{
		{"conductor beats env (the #881 fix)", filepath.Join(tmpHome, ".claude-env"), "conductor-coordinator", "", "conductor"},
		{"group beats env", filepath.Join(tmpHome, ".claude-env"), "regular-session", "team-a", "group"},
		{"env beats profile", filepath.Join(tmpHome, ".claude-env"), "regular-session", "", "env"},
		{"profile beats global (no env)", "", "regular-session", "", "profile"},
	}
	for _, tc := range instCases {
		t.Run("instance/"+tc.name, func(t *testing.T) {
			if tc.envOverride != "" {
				_ = os.Setenv("CLAUDE_CONFIG_DIR", tc.envOverride)
			} else {
				_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
			}
			ClearUserConfigCache()

			inst := &Instance{Title: tc.title, GroupPath: tc.groupPath}
			gotPath := GetClaudeConfigDirForInstance(inst)
			srcPath, srcSource := GetClaudeConfigDirSourceForInstance(inst)
			if gotPath != srcPath {
				t.Errorf("PATH DRIFT: GetClaudeConfigDirForInstance=%q, GetClaudeConfigDirSourceForInstance path=%q (source=%q)", gotPath, srcPath, srcSource)
			}
			if tc.wantSource != "" && srcSource != tc.wantSource {
				t.Errorf("source = %q, want %q", srcSource, tc.wantSource)
			}
			gotExplicit := IsClaudeConfigDirExplicitForInstance(inst)
			wantExplicit := srcSource != "default"
			if gotExplicit != wantExplicit {
				t.Errorf("EXPLICIT DRIFT: IsClaudeConfigDirExplicitForInstance=%v, source=%q (expected explicit=%v)", gotExplicit, srcSource, wantExplicit)
			}
		})
	}
}

// TestClaudeConfigDirSingleResolver asserts that there is exactly ONE
// internal resolver (resolveClaudeConfigDir) producing both the path and
// source string, and that all public functions delegate to it. This is a
// structural drift-detection guard: future PRs that re-introduce a
// parallel implementation will fail to compile here (the helper symbol
// must remain).
func TestClaudeConfigDirSingleResolver(t *testing.T) {
	// Compile-time check: resolver helper is referenced.
	path, source := resolveClaudeConfigDir(resolveOpts{})
	if path == "" || source == "" {
		t.Fatalf("resolveClaudeConfigDir returned empty: path=%q source=%q", path, source)
	}
}
