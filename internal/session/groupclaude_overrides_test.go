package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for the extended [groups.X.claude] / [conductors.X.claude] key
// surface: command, model, inline env map (skills/mcps union).
// Reuses withIsolatedHomeAndConfig from pergroupconfig_nested_test.go.
// Spawn-path loudness tests (missing env_file, sticky config.toml error) live
// in userconfig_loudness_test.go in the companion fix/userconfig-sticky-error
// branch.

func TestGroupConductorClaude_CommandResolution(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[claude]
command = "claude-global"

[groups."work".claude]
command = "claude-work"

[conductors.lilu.claude]
command = "claude-lilu"
`)

	cases := []struct {
		name  string
		title string
		group string
		want  string
	}{
		{"group exact", "s1", "work", "claude-work"},
		{"group ancestor walk", "s2", "work/sub/deep", "claude-work"},
		{"conductor beats group", "conductor-lilu", "work", "claude-lilu"},
		{"global fallback", "s3", "other", "claude-global"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inst := NewInstanceWithGroupAndTool(tc.title, "/tmp/p", tc.group, "claude")
			if got := GetClaudeCommandForInstance(inst); got != tc.want {
				t.Errorf("GetClaudeCommandForInstance=%q want %q", got, tc.want)
			}
			cmd := inst.buildClaudeCommand("claude")
			if !strings.Contains(cmd, tc.want+" --session-id") {
				t.Errorf("spawn command does not exec %q:\n%s", tc.want, cmd)
			}
		})
	}
}

func TestGroupConductorClaude_CommandDefaultsToClaudeWithoutConfig(t *testing.T) {
	withIsolatedHomeAndConfig(t, ``)
	inst := NewInstanceWithGroupAndTool("s1", "/tmp/p", "work", "claude")
	if got := GetClaudeCommandForInstance(inst); got != "claude" {
		t.Errorf("GetClaudeCommandForInstance=%q want claude", got)
	}
}

func TestGroupConductorClaude_ModelResolution(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[groups."work".claude]
model = "claude-sonnet-4-6"

[conductors.lilu.claude]
model = "claude-opus-4-8"
`)

	t.Run("group model applies when session has none", func(t *testing.T) {
		inst := NewInstanceWithGroupAndTool("s1", "/tmp/p", "work/sub", "claude")
		cmd := inst.buildClaudeCommand("claude")
		if !strings.Contains(cmd, "--model claude-sonnet-4-6") {
			t.Errorf("expected group model flag in command:\n%s", cmd)
		}
	})

	t.Run("conductor model beats group model", func(t *testing.T) {
		inst := NewInstanceWithGroupAndTool("conductor-lilu", "/tmp/p", "work", "claude")
		cmd := inst.buildClaudeCommand("claude")
		if !strings.Contains(cmd, "--model claude-opus-4-8") {
			t.Errorf("expected conductor model flag in command:\n%s", cmd)
		}
	})

	t.Run("explicit per-session model wins", func(t *testing.T) {
		inst := NewInstanceWithGroupAndTool("s2", "/tmp/p", "work", "claude")
		if err := inst.SetClaudeOptions(&ClaudeOptions{SessionMode: "new", Model: "claude-haiku-4-5"}); err != nil {
			t.Fatalf("SetClaudeOptions: %v", err)
		}
		cmd := inst.buildClaudeCommand("claude")
		if !strings.Contains(cmd, "--model claude-haiku-4-5") {
			t.Errorf("expected per-session model flag in command:\n%s", cmd)
		}
		if strings.Contains(cmd, "claude-sonnet-4-6") {
			t.Errorf("group model must not override per-session model:\n%s", cmd)
		}
	})

	t.Run("no model levels set emits no flag", func(t *testing.T) {
		inst := NewInstanceWithGroupAndTool("s3", "/tmp/p", "other", "claude")
		cmd := inst.buildClaudeCommand("claude")
		if strings.Contains(cmd, "--model") {
			t.Errorf("expected no --model flag (empty falls through, #1172 semantics):\n%s", cmd)
		}
	})
}

func TestGroupConductorClaude_InlineEnvLayering(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[groups."personal".claude]
env = { AGENT_ROLE = "parent", SHARED = "from-parent" }

[groups."personal/sub".claude]
env = { AGENT_ROLE = "child" }

[conductors.lilu.claude]
env = { AGENT_ROLE = "lilu", LILU_ONLY = "1" }
`)

	t.Run("child group key wins, parent-only key persists", func(t *testing.T) {
		inst := NewInstanceWithGroupAndTool("s1", "/tmp/p", "personal/sub", "claude")
		cmd := inst.buildEnvSourceCommand()
		if !strings.Contains(cmd, "export AGENT_ROLE='child'") {
			t.Errorf("nearest group must win per key:\n%s", cmd)
		}
		if !strings.Contains(cmd, "export SHARED='from-parent'") {
			t.Errorf("parent-only key must persist through the merge:\n%s", cmd)
		}
	})

	t.Run("conductor env wins over group env per key", func(t *testing.T) {
		inst := NewInstanceWithGroupAndTool("conductor-lilu", "/tmp/p", "personal/sub", "claude")
		cmd := inst.buildEnvSourceCommand()
		for _, want := range []string{"export AGENT_ROLE='lilu'", "export LILU_ONLY='1'", "export SHARED='from-parent'"} {
			if !strings.Contains(cmd, want) {
				t.Errorf("missing %q in:\n%s", want, cmd)
			}
		}
	})

	t.Run("non-claude tools get no claude inline env", func(t *testing.T) {
		inst := NewInstanceWithGroupAndTool("s2", "/tmp/p", "personal/sub", "codex")
		cmd := inst.buildEnvSourceCommand()
		if strings.Contains(cmd, "AGENT_ROLE") {
			t.Errorf("[groups.X.claude].env must not leak into codex spawns:\n%s", cmd)
		}
	})
}

func TestGroupClaude_InlineEnvExportedAfterEnvFile(t *testing.T) {
	tmpHome := withIsolatedHomeAndConfig(t, `
[groups."personal".claude]
env_file = "~/.agent-deck/personal.env"
env = { AGENT_ROLE = "inline-wins" }
`)
	// Create the env_file so no missing-file warning muddies the ordering check.
	envPath := filepath.Join(tmpHome, ".agent-deck", "personal.env")
	if err := os.WriteFile(envPath, []byte("export AGENT_ROLE=from-file\n"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	inst := NewInstanceWithGroupAndTool("s1", "/tmp/p", "personal", "claude")
	cmd := inst.buildEnvSourceCommand()

	sourceIdx := strings.Index(cmd, `source "`+envPath+`"`)
	exportIdx := strings.Index(cmd, "export AGENT_ROLE='inline-wins'")
	if sourceIdx == -1 || exportIdx == -1 {
		t.Fatalf("missing env_file source (%d) or inline export (%d) in:\n%s", sourceIdx, exportIdx, cmd)
	}
	if exportIdx < sourceIdx {
		t.Errorf("inline env must be exported AFTER the env_file source so it wins on conflict:\n%s", cmd)
	}
}

func TestRestartEnvOverridesConfiguredInlineEnv(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[groups."personal".claude]
env = { AGENT_ROLE = "configured" }
`)
	inst := NewInstanceWithGroupAndTool("s1", "/tmp/p", "personal", "claude")
	inst.restartEnv = map[string]string{"AGENT_ROLE": "restart"}
	cmd := inst.buildEnvSourceCommand()

	configuredIdx := strings.Index(cmd, "export AGENT_ROLE='configured'")
	restartIdx := strings.Index(cmd, "export AGENT_ROLE='restart'")
	if configuredIdx == -1 || restartIdx == -1 {
		t.Fatalf("missing configured (%d) or restart (%d) export in:\n%s", configuredIdx, restartIdx, cmd)
	}
	if restartIdx < configuredIdx {
		t.Errorf("restart env must be exported after configured env so it wins:\n%s", cmd)
	}
}

func TestGroupClaude_InlineEnvSkipsInvalidKeysAndEscapesQuotes(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[groups."work".claude]
env = { "BAD-KEY" = "x", GOOD = "it's" }
`)
	inst := NewInstanceWithGroupAndTool("s1", "/tmp/p", "work", "claude")
	cmd := inst.buildEnvSourceCommand()
	if strings.Contains(cmd, "BAD-KEY") {
		t.Errorf("invalid env var name must be skipped:\n%s", cmd)
	}
	if !strings.Contains(cmd, `export GOOD='it'\''s'`) {
		t.Errorf("single quotes in values must be escaped:\n%s", cmd)
	}
}

func TestGroupClaude_SkillsAndMCPsUnionAlongAncestors(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[groups."work".claude]
skills = ["store/base", "store/shared"]
mcps = ["memory"]

[groups."work/sub".claude]
skills = ["store/extra", "store/shared"]
mcps = ["exa"]
`)
	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}

	skills := cfg.GetGroupClaudeSkills("work/sub/leaf")
	wantSkills := []string{"store/base", "store/shared", "store/extra"}
	if strings.Join(skills, ",") != strings.Join(wantSkills, ",") {
		t.Errorf("skills union=%v want %v (root-first, deduped — floor semantics)", skills, wantSkills)
	}

	mcps := cfg.GetGroupClaudeMCPs("work/sub")
	wantMCPs := []string{"memory", "exa"}
	if strings.Join(mcps, ",") != strings.Join(wantMCPs, ",") {
		t.Errorf("mcps union=%v want %v", mcps, wantMCPs)
	}

	if got := cfg.GetGroupClaudeSkills("unrelated"); got != nil {
		t.Errorf("unrelated group must have no skills, got %v", got)
	}
}

func TestResolveGroupClaude_SourcesAndEnvFileExistence(t *testing.T) {
	tmpHome := withIsolatedHomeAndConfig(t, `
[claude]
command = "claude-global"

[groups."work".claude]
env_file = "~/.agent-deck/groups/work.env"
model = "claude-sonnet-4-6"
env = { AGENT_ROLE = "work" }
skills = ["store/loom"]
`)

	res := ResolveGroupClaude("work/sub")
	if res.ConfigError != "" {
		t.Fatalf("unexpected config error: %s", res.ConfigError)
	}
	if res.EnvFileSource != "group:work" {
		t.Errorf("env_file source=%q want group:work", res.EnvFileSource)
	}
	if res.EnvFileExists {
		t.Error("env_file must report missing before the file is created")
	}
	if res.Command != "claude-global" || res.CommandSource != "global" {
		t.Errorf("command=%q [%s] want claude-global [global]", res.Command, res.CommandSource)
	}
	if res.Model != "claude-sonnet-4-6" || res.ModelSource != "group:work" {
		t.Errorf("model=%q [%s] want claude-sonnet-4-6 [group:work]", res.Model, res.ModelSource)
	}
	if res.Env["AGENT_ROLE"] != "work" {
		t.Errorf("env=%v want AGENT_ROLE=work", res.Env)
	}
	if len(res.Skills) != 1 || res.Skills[0] != "store/loom" {
		t.Errorf("skills=%v want [store/loom]", res.Skills)
	}

	envPath := filepath.Join(tmpHome, ".agent-deck", "groups", "work.env")
	if err := os.MkdirAll(filepath.Dir(envPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(envPath, []byte("export A=1\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	res = ResolveGroupClaude("work")
	if !res.EnvFileExists {
		t.Error("env_file must report exists after creation")
	}
}

func TestResolveGroupClaude_SurfacesConfigError(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[groups."work".claude
broken =
`)
	res := ResolveGroupClaude("work")
	if res.ConfigError == "" {
		t.Fatal("a broken config.toml must surface in ConfigError — this is the zero-diagnostics failure mode group show --resolved exists to catch")
	}
	if res.Command != "claude" || res.CommandSource != "default" {
		t.Errorf("broken config must resolve to defaults, got command=%q [%s]", res.Command, res.CommandSource)
	}
}
