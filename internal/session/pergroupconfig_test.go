package session

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestPerGroupConfig_CustomCommandGetsGroupConfigDir locks CFG-02: a
// custom-command (wrapper-script) claude session spawn command must export
// CLAUDE_CONFIG_DIR from the group's Claude config_dir override.
//
// Expected RED against base fa9971e: buildClaudeCommandWithMessage returns
// baseCommand unchanged at instance.go:596 when baseCommand != "claude",
// so CLAUDE_CONFIG_DIR never reaches the wrapper's exec env.
func TestPerGroupConfig_CustomCommandGetsGroupConfigDir(t *testing.T) {
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
	cfg := `
[groups."conductor".claude]
config_dir = "~/.claude-work"
`
	if err := os.WriteFile(filepath.Join(agentDeckDir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()

	inst := NewInstanceWithGroupAndTool("conductor-x", "/tmp/p", "conductor", "claude")
	wrapper := "/tmp/start-conductor.sh"
	cmd := inst.buildClaudeCommand(wrapper)

	wantDir := filepath.Join(tmpHome, ".claude-work")
	if !strings.Contains(cmd, "CLAUDE_CONFIG_DIR="+wantDir) {
		t.Errorf("custom-command spawn missing CLAUDE_CONFIG_DIR=%s\ngot: %s", wantDir, cmd)
	}
	if !strings.HasSuffix(cmd, wrapper) {
		t.Errorf("spawn must end with wrapper path %q, got: %s", wrapper, cmd)
	}
}

// TestPerGroupConfig_GroupOverrideBeatsProfile locks CFG-04 test 2: when
// both a profile-level and group-level Claude config_dir are set, the group
// value wins in the spawn command for a custom-command session.
//
// Expected RED against base fa9971e (same root cause as test 1).
func TestPerGroupConfig_GroupOverrideBeatsProfile(t *testing.T) {
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	origProfile := os.Getenv("AGENTDECK_PROFILE")
	origEnvDir := os.Getenv("CLAUDE_CONFIG_DIR")
	t.Cleanup(func() {
		_ = os.Setenv("HOME", origHome)
		if origProfile != "" {
			_ = os.Setenv("AGENTDECK_PROFILE", origProfile)
		} else {
			_ = os.Unsetenv("AGENTDECK_PROFILE")
		}
		if origEnvDir != "" {
			_ = os.Setenv("CLAUDE_CONFIG_DIR", origEnvDir)
		} else {
			_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
		}
		ClearUserConfigCache()
	})

	_ = os.Setenv("HOME", tmpHome)
	_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
	_ = os.Setenv("AGENTDECK_PROFILE", "work")

	agentDeckDir := filepath.Join(tmpHome, ".agent-deck")
	if err := os.MkdirAll(agentDeckDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := `
[profiles.work.claude]
config_dir = "~/.claude-work"

[groups."conductor".claude]
config_dir = "~/.claude-group"
`
	if err := os.WriteFile(filepath.Join(agentDeckDir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()

	inst := NewInstanceWithGroupAndTool("c", "/tmp/p", "conductor", "claude")
	cmd := inst.buildClaudeCommand("/tmp/wrapper.sh")

	wantGroup := filepath.Join(tmpHome, ".claude-group")
	if !strings.Contains(cmd, "CLAUDE_CONFIG_DIR="+wantGroup) {
		t.Errorf("group override must beat profile; want CLAUDE_CONFIG_DIR=%s, got: %s", wantGroup, cmd)
	}
	profilePath := filepath.Join(tmpHome, ".claude-work")
	if strings.Contains(cmd, "CLAUDE_CONFIG_DIR="+profilePath) {
		t.Errorf("profile path leaked into spawn despite group override; got: %s", cmd)
	}
}

// TestPerGroupConfig_UnknownGroupFallsThroughToProfile locks CFG-04 test 3:
// an unknown group name resolves to the profile-level Claude config_dir.
//
// Expected GREEN immediately against base fa9971e (resolver already correct
// per PR #578).
func TestPerGroupConfig_UnknownGroupFallsThroughToProfile(t *testing.T) {
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	origProfile := os.Getenv("AGENTDECK_PROFILE")
	origEnvDir := os.Getenv("CLAUDE_CONFIG_DIR")
	t.Cleanup(func() {
		_ = os.Setenv("HOME", origHome)
		if origProfile != "" {
			_ = os.Setenv("AGENTDECK_PROFILE", origProfile)
		} else {
			_ = os.Unsetenv("AGENTDECK_PROFILE")
		}
		if origEnvDir != "" {
			_ = os.Setenv("CLAUDE_CONFIG_DIR", origEnvDir)
		} else {
			_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
		}
		ClearUserConfigCache()
	})

	_ = os.Setenv("HOME", tmpHome)
	_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
	_ = os.Setenv("AGENTDECK_PROFILE", "work")

	agentDeckDir := filepath.Join(tmpHome, ".agent-deck")
	if err := os.MkdirAll(agentDeckDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := `
[profiles.work.claude]
config_dir = "~/.claude-work"

[groups."real-group".claude]
config_dir = "~/.claude-real-group"
`
	if err := os.WriteFile(filepath.Join(agentDeckDir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()

	got := GetClaudeConfigDirForGroup("does-not-exist")
	want := filepath.Join(tmpHome, ".claude-work")
	if got != want {
		t.Errorf("unknown group should fall through to profile: got=%s want=%s", got, want)
	}
}

// TestPerGroupConfig_CacheInvalidation locks CFG-04 test 6: rewriting the
// on-disk config.toml followed by ClearUserConfigCache() causes the resolver
// to return the new value (or the default when the override is removed).
//
// Expected GREEN immediately against base fa9971e.
func TestPerGroupConfig_CacheInvalidation(t *testing.T) {
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	origProfile := os.Getenv("AGENTDECK_PROFILE")
	origEnvDir := os.Getenv("CLAUDE_CONFIG_DIR")
	t.Cleanup(func() {
		_ = os.Setenv("HOME", origHome)
		if origProfile != "" {
			_ = os.Setenv("AGENTDECK_PROFILE", origProfile)
		} else {
			_ = os.Unsetenv("AGENTDECK_PROFILE")
		}
		if origEnvDir != "" {
			_ = os.Setenv("CLAUDE_CONFIG_DIR", origEnvDir)
		} else {
			_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
		}
		ClearUserConfigCache()
	})

	_ = os.Setenv("HOME", tmpHome)
	_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
	_ = os.Unsetenv("AGENTDECK_PROFILE")

	agentDeckDir := filepath.Join(tmpHome, ".agent-deck")
	configPath := filepath.Join(agentDeckDir, "config.toml")
	if err := os.MkdirAll(agentDeckDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// v1: group override present
	v1 := `[groups."g".claude]
config_dir = "~/.claude-g"
`
	if err := os.WriteFile(configPath, []byte(v1), 0o600); err != nil {
		t.Fatalf("write v1 config: %v", err)
	}
	ClearUserConfigCache()
	if got, want := GetClaudeConfigDirForGroup("g"), filepath.Join(tmpHome, ".claude-g"); got != want {
		t.Fatalf("v1: got %s want %s", got, want)
	}

	// v2: group override removed; cache must be cleared to pick up the change
	v2 := "# empty config\n"
	if err := os.WriteFile(configPath, []byte(v2), 0o600); err != nil {
		t.Fatalf("write v2 config: %v", err)
	}
	ClearUserConfigCache()
	got := GetClaudeConfigDirForGroup("g")
	want := filepath.Join(tmpHome, ".claude") // default when no override
	if got != want {
		t.Errorf("after cache invalidation, got=%s want=%s", got, want)
	}
}

// TestPerGroupConfig_EnvFileSourcedInSpawn locks CFG-03 + CFG-04 test 4:
// a group-specific env_file must be sourced in the production
// spawn-command builder for BOTH the normal-claude path and the
// custom-command path before the wrapper exec's.
//
// Why assert on buildClaudeCommand (and not buildEnvSourceCommand directly):
// the production spawn pipeline is buildClaudeCommand at instance.go:477.
// Asserting on buildEnvSourceCommand alone would prove the builder works
// but NOT prove the production spawn path invokes it. The custom-command
// return at instance.go:598 is known to be `buildBashExportPrefix() + baseCommand`
// — it does NOT prepend buildEnvSourceCommand(). Assertion B is expected
// to RED on first run; the fix lands at instance.go:598.
func TestPerGroupConfig_EnvFileSourcedInSpawn(t *testing.T) {
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

	// Arrange: envrc-test with sentinel export
	envrcPath := filepath.Join(tmpHome, "envrc-test")
	if err := os.WriteFile(envrcPath, []byte("export TEST_ENVFILE_VAR=hello\n"), 0o600); err != nil {
		t.Fatalf("write envrc: %v", err)
	}

	// Arrange: ~/.agent-deck/config.toml with group env_file
	agentDeckDir := filepath.Join(tmpHome, ".agent-deck")
	if err := os.MkdirAll(agentDeckDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := fmt.Sprintf(`
[groups."envfile-grp".claude]
env_file = "%s"
`, envrcPath)
	if err := os.WriteFile(filepath.Join(agentDeckDir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()

	wantSource := `source "` + envrcPath + `"`

	// Assertion A (normal-claude branch at instance.go:478):
	// Build via the normal-claude path. Expected GREEN on first run.
	instNormal := NewInstanceWithGroupAndTool("envfile-normal", tmpHome, "envfile-grp", "claude")
	cmdNormal := instNormal.buildClaudeCommand("claude")
	if !strings.Contains(cmdNormal, wantSource) {
		t.Errorf("normal-claude spawn command missing env_file source line\nwant substring: %s\ngot: %s", wantSource, cmdNormal)
	}

	// Assertion B (custom-command branch at instance.go:598):
	// Build via the custom-command path. Expected RED on first run against
	// base 4730aa5 — instance.go:598 returns
	//   `i.buildBashExportPrefix() + baseCommand`
	// and does NOT prepend buildEnvSourceCommand(). The fix lands at
	// instance.go:598 in a follow-up commit.
	instCustom := NewInstanceWithGroupAndTool("envfile-custom", tmpHome, "envfile-grp", "claude")
	instCustom.Command = "bash -c 'exec claude'"
	cmdCustom := instCustom.buildClaudeCommand(instCustom.Command)
	if !strings.Contains(cmdCustom, wantSource) {
		t.Errorf("custom-command spawn command missing env_file source line (CFG-03 gap at instance.go:598)\nwant substring: %s\ngot: %s", wantSource, cmdCustom)
	}

	// Assertion C (runtime proof on the custom-command path):
	// Execute the full built command under bash with the payload swapped
	// for an echo of the sentinel var. Only runs if assertion B passed.
	if strings.Contains(cmdCustom, wantSource) {
		// Replace the trailing payload (bash -c 'exec claude') with a sentinel echo.
		// The source line will have run, so `echo "$TEST_ENVFILE_VAR"` should print "hello".
		idx := strings.LastIndex(cmdCustom, "bash -c 'exec claude'")
		if idx == -1 {
			t.Fatalf("runtime proof: could not locate custom-command payload in built cmd: %s", cmdCustom)
		}
		harness := cmdCustom[:idx] + `echo "$TEST_ENVFILE_VAR"`
		out, err := exec.Command("bash", "-c", harness).CombinedOutput()
		if err != nil {
			t.Fatalf("runtime proof bash exec failed: %v\noutput: %s\nharness: %s", err, string(out), harness)
		}
		got := strings.TrimSpace(string(out))
		if got != "hello" {
			t.Errorf("runtime proof: env_file not sourced into spawn env on custom-command path\nwant TEST_ENVFILE_VAR=hello, got %q\nharness: %s", got, harness)
		}
	}

	// Negative case: remove the env_file override, cache-bust, rebuild both commands — path must NOT appear
	cfgEmpty := "# empty\n"
	if err := os.WriteFile(filepath.Join(agentDeckDir, "config.toml"), []byte(cfgEmpty), 0o600); err != nil {
		t.Fatalf("rewrite empty config: %v", err)
	}
	ClearUserConfigCache()
	instNormal2 := NewInstanceWithGroupAndTool("envfile-normal2", tmpHome, "envfile-grp", "claude")
	cmdNormal2 := instNormal2.buildClaudeCommand("claude")
	if strings.Contains(cmdNormal2, envrcPath) {
		t.Errorf("negative case (normal): after removing env_file, cmd must NOT reference %q; got: %s", envrcPath, cmdNormal2)
	}
	instCustom2 := NewInstanceWithGroupAndTool("envfile-custom2", tmpHome, "envfile-grp", "claude")
	instCustom2.Command = "bash -c 'exec claude'"
	cmdCustom2 := instCustom2.buildClaudeCommand(instCustom2.Command)
	if strings.Contains(cmdCustom2, envrcPath) {
		t.Errorf("negative case (custom): after removing env_file, cmd must NOT reference %q; got: %s", envrcPath, cmdCustom2)
	}

	// Missing-file case: point env_file at a non-existent file — must not block,
	// must still appear with ignore-missing guard, sentinel is empty at runtime.
	missingPath := filepath.Join(tmpHome, "does-not-exist.envrc")
	cfgMissing := fmt.Sprintf(`
[groups."envfile-grp".claude]
env_file = "%s"
`, missingPath)
	if err := os.WriteFile(filepath.Join(agentDeckDir, "config.toml"), []byte(cfgMissing), 0o600); err != nil {
		t.Fatalf("rewrite missing-file config: %v", err)
	}
	ClearUserConfigCache()
	instNormal3 := NewInstanceWithGroupAndTool("envfile-normal3", tmpHome, "envfile-grp", "claude")
	cmdNormal3 := instNormal3.buildClaudeCommand("claude")
	if !strings.Contains(cmdNormal3, missingPath) {
		t.Errorf("missing-file (normal): cmd should still reference path %q; got: %s", missingPath, cmdNormal3)
	}
}

// TestPerGroupConfig_ConductorRestartPreservesConfigDir locks CFG-04 test 5:
// a custom-command (conductor) session with a group config_dir override
// receives the same CLAUDE_CONFIG_DIR export in its spawn command after a
// simulated stop-then-restart (cache clear + rebuild). Proves the restart
// loop in v1.5.2's REQ-7 is honored for per-group config overrides.
//
// Pure build-and-assert; no tmux, no real process spawn.
func TestPerGroupConfig_ConductorRestartPreservesConfigDir(t *testing.T) {
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	origProfile := os.Getenv("AGENTDECK_PROFILE")
	origEnvDir := os.Getenv("CLAUDE_CONFIG_DIR")
	t.Cleanup(func() {
		_ = os.Setenv("HOME", origHome)
		if origProfile != "" {
			_ = os.Setenv("AGENTDECK_PROFILE", origProfile)
		} else {
			_ = os.Unsetenv("AGENTDECK_PROFILE")
		}
		if origEnvDir != "" {
			_ = os.Setenv("CLAUDE_CONFIG_DIR", origEnvDir)
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
	cfg := `
[groups."conductor".claude]
config_dir = "~/.claude-work"
`
	if err := os.WriteFile(filepath.Join(agentDeckDir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()

	wantDir := filepath.Join(tmpHome, ".claude-work")
	wrapper := "/tmp/start-conductor.sh"

	inst1 := NewInstanceWithGroupAndTool("conductor-a", tmpHome, "conductor", "claude")
	cmd1 := inst1.buildClaudeCommand(wrapper)
	if !strings.Contains(cmd1, "CLAUDE_CONFIG_DIR="+wantDir) {
		t.Fatalf("first spawn missing CLAUDE_CONFIG_DIR=%s\ngot: %s", wantDir, cmd1)
	}

	ClearUserConfigCache()
	inst2 := NewInstanceWithGroupAndTool("conductor-b", tmpHome, "conductor", "claude")
	cmd2 := inst2.buildClaudeCommand(wrapper)
	if !strings.Contains(cmd2, "CLAUDE_CONFIG_DIR="+wantDir) {
		t.Fatalf("post-restart spawn missing CLAUDE_CONFIG_DIR=%s\ngot: %s", wantDir, cmd2)
	}

	re := regexp.MustCompile(`export CLAUDE_CONFIG_DIR=([^;]+);`)
	m1 := re.FindStringSubmatch(cmd1)
	m2 := re.FindStringSubmatch(cmd2)
	if m1 == nil || m2 == nil {
		t.Fatalf("could not extract CLAUDE_CONFIG_DIR export from one or both spawns\ncmd1=%s\ncmd2=%s", cmd1, cmd2)
	}
	if m1[1] != m2[1] {
		t.Errorf("CLAUDE_CONFIG_DIR drifted across restart: pre=%q post=%q", m1[1], m2[1])
	}
	if !strings.HasSuffix(cmd1, wrapper) || !strings.HasSuffix(cmd2, wrapper) {
		t.Errorf("both spawns must end with wrapper %q\ncmd1=%s\ncmd2=%s", wrapper, cmd1, cmd2)
	}
}

// TestPerGroupConfig_ClaudeConfigDirSourceLabel locks CFG-07's source-label
// mapping against the priority chain in GetClaudeConfigDirForGroup at
// claude.go:246. Exercises all 5 priority levels: env, group, profile,
// global, default.
//
// Expected RED against the stub: all 5 subtests fail (stub returns "", "").
// Task 2 replaces the stub body and turns GREEN.
func TestPerGroupConfig_ClaudeConfigDirSourceLabel(t *testing.T) {
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	origProfile := os.Getenv("AGENTDECK_PROFILE")
	origEnvDir := os.Getenv("CLAUDE_CONFIG_DIR")
	t.Cleanup(func() {
		_ = os.Setenv("HOME", origHome)
		if origProfile != "" {
			_ = os.Setenv("AGENTDECK_PROFILE", origProfile)
		} else {
			_ = os.Unsetenv("AGENTDECK_PROFILE")
		}
		if origEnvDir != "" {
			_ = os.Setenv("CLAUDE_CONFIG_DIR", origEnvDir)
		} else {
			_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
		}
		ClearUserConfigCache()
	})

	agentDeckDir := filepath.Join(tmpHome, ".agent-deck")
	configPath := filepath.Join(agentDeckDir, "config.toml")
	if err := os.MkdirAll(agentDeckDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeConfig := func(t *testing.T, body string) {
		t.Helper()
		if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}
	_ = os.Setenv("HOME", tmpHome)

	t.Run("group_wins_over_env", func(t *testing.T) {
		// #1508: a config.toml group override is strictly more specific than
		// a shell-wide CLAUDE_CONFIG_DIR, so the group must win when it has a
		// config_dir to assert.
		_ = os.Setenv("CLAUDE_CONFIG_DIR", "/tmp/env-dir")
		_ = os.Setenv("AGENTDECK_PROFILE", "p")
		writeConfig(t, `
[groups."g".claude]
config_dir = "~/.claude-g"
[profiles.p.claude]
config_dir = "~/.claude-p"
[claude]
config_dir = "~/.claude-global"
`)
		ClearUserConfigCache()
		path, source := GetClaudeConfigDirSourceForGroup("g")
		if source != "group" {
			t.Errorf("source=%q want %q", source, "group")
		}
		if want := filepath.Join(tmpHome, ".claude-g"); path != want {
			t.Errorf("path=%q want %q", path, want)
		}
		_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
	})

	t.Run("env_var_wins_without_group_match", func(t *testing.T) {
		// #1508: env still wins when the group has no config_dir to assert.
		_ = os.Setenv("CLAUDE_CONFIG_DIR", "/tmp/env-dir")
		_ = os.Setenv("AGENTDECK_PROFILE", "p")
		writeConfig(t, `
[groups."g".claude]
config_dir = "~/.claude-g"
[profiles.p.claude]
config_dir = "~/.claude-p"
[claude]
config_dir = "~/.claude-global"
`)
		ClearUserConfigCache()
		path, source := GetClaudeConfigDirSourceForGroup("unknown-group")
		if source != "env" {
			t.Errorf("source=%q want %q", source, "env")
		}
		if path != "/tmp/env-dir" {
			t.Errorf("path=%q want %q", path, "/tmp/env-dir")
		}
		_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
	})

	t.Run("group_wins_over_profile_global", func(t *testing.T) {
		_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
		_ = os.Setenv("AGENTDECK_PROFILE", "p")
		writeConfig(t, `
[groups."g".claude]
config_dir = "~/.claude-g"
[profiles.p.claude]
config_dir = "~/.claude-p"
[claude]
config_dir = "~/.claude-global"
`)
		ClearUserConfigCache()
		path, source := GetClaudeConfigDirSourceForGroup("g")
		if source != "group" {
			t.Errorf("source=%q want %q", source, "group")
		}
		if want := filepath.Join(tmpHome, ".claude-g"); path != want {
			t.Errorf("path=%q want %q", path, want)
		}
	})

	t.Run("profile_wins_over_global", func(t *testing.T) {
		_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
		_ = os.Setenv("AGENTDECK_PROFILE", "p")
		writeConfig(t, `
[profiles.p.claude]
config_dir = "~/.claude-p"
[claude]
config_dir = "~/.claude-global"
`)
		ClearUserConfigCache()
		path, source := GetClaudeConfigDirSourceForGroup("unknown-group")
		if source != "profile" {
			t.Errorf("source=%q want %q", source, "profile")
		}
		if want := filepath.Join(tmpHome, ".claude-p"); path != want {
			t.Errorf("path=%q want %q", path, want)
		}
	})

	t.Run("global_fallback", func(t *testing.T) {
		_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
		_ = os.Unsetenv("AGENTDECK_PROFILE")
		writeConfig(t, `
[claude]
config_dir = "~/.claude-global"
`)
		ClearUserConfigCache()
		path, source := GetClaudeConfigDirSourceForGroup("")
		if source != "global" {
			t.Errorf("source=%q want %q", source, "global")
		}
		if want := filepath.Join(tmpHome, ".claude-global"); path != want {
			t.Errorf("path=%q want %q", path, want)
		}
	})

	t.Run("default_fallback", func(t *testing.T) {
		_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
		_ = os.Unsetenv("AGENTDECK_PROFILE")
		writeConfig(t, "# empty\n")
		ClearUserConfigCache()
		path, source := GetClaudeConfigDirSourceForGroup("")
		if source != "default" {
			t.Errorf("source=%q want %q", source, "default")
		}
		if want := filepath.Join(tmpHome, ".claude"); path != want {
			t.Errorf("path=%q want %q", path, want)
		}
	})
}

// TestPerGroupConfig_ClaudeConfigResolutionLogFormat locks the CFG-07
// slog output format against the spec's rendered form:
//
//	claude config resolution session=<id> group=<g> resolved=<path> source=<label>
//
// Swaps sessionLog's handler for a bytes.Buffer-backed slog.NewTextHandler,
// calls (*Instance).logClaudeConfigResolution() on a known instance, and
// regex-matches the rendered line.
//
// Expected RED against the stub logClaudeConfigResolution: buffer is empty,
// regex does not match. Task 2 fills in the helper body and turns GREEN.
func TestPerGroupConfig_ClaudeConfigResolutionLogFormat(t *testing.T) {
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	origEnvDir := os.Getenv("CLAUDE_CONFIG_DIR")
	origProfile := os.Getenv("AGENTDECK_PROFILE")
	t.Cleanup(func() {
		_ = os.Setenv("HOME", origHome)
		if origEnvDir != "" {
			_ = os.Setenv("CLAUDE_CONFIG_DIR", origEnvDir)
		} else {
			_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
		}
		if origProfile != "" {
			_ = os.Setenv("AGENTDECK_PROFILE", origProfile)
		} else {
			_ = os.Unsetenv("AGENTDECK_PROFILE")
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
	if err := os.WriteFile(
		filepath.Join(agentDeckDir, "config.toml"),
		[]byte(`[groups."logfmt".claude]`+"\n"+`config_dir = "~/.claude-logfmt"`+"\n"),
		0o600,
	); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()

	// Swap the package-level sessionLog for a buffer-backed TextHandler.
	var buf bytes.Buffer
	testLogger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	origLog := sessionLog
	sessionLog = testLogger
	t.Cleanup(func() { sessionLog = origLog })

	inst := NewInstanceWithGroupAndTool("logfmt-sess-123", tmpHome, "logfmt", "claude")
	// NewInstanceWithGroupAndTool sets Title from the first arg but assigns a
	// generated UUID-based ID. The CFG-07 log line keys on i.ID (not Title),
	// so override the ID here to make the session= assertion below precise.
	inst.ID = "logfmt-sess-123"
	inst.logClaudeConfigResolution()

	line := buf.String()
	if !strings.Contains(line, "claude config resolution") {
		t.Fatalf("missing CFG-07 message literal in rendered log\ngot: %s", line)
	}
	formatRe := regexp.MustCompile(`claude config resolution.*session=\S+\s+group=\S*\s+resolved=\S+\s+source=(env|group|profile|global|default)`)
	if !formatRe.MatchString(line) {
		t.Errorf("CFG-07 rendered log does not match spec format\nregex: %s\ngot:   %s", formatRe.String(), line)
	}
	if !strings.Contains(line, "session=logfmt-sess-123") &&
		!strings.Contains(line, `session="logfmt-sess-123"`) {
		t.Errorf("expected session id in log; got: %s", line)
	}
	if !strings.Contains(line, "source=group") &&
		!strings.Contains(line, `source="group"`) {
		t.Errorf("expected source=group for [groups.\"logfmt\".claude] override; got: %s", line)
	}
}
