package session

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestResolveGroupCodex_InheritsAndPrefersNearestScalar(t *testing.T) {
	tmpHome := withIsolatedHomeAndConfig(t, `
[codex]
config_dir = "~/.codex-global"
command = "codex-global"

[groups."work".codex]
config_dir = "~/.codex-work"
command = "codex-work"
skills = ["store/base", "store/shared"]
mcps = ["memory"]

[groups."work/sub".codex]
config_dir = "~/.codex-work-sub"
skills = ["store/extra", "store/shared"]
mcps = ["exa", "memory"]
`)

	res := ResolveGroupCodex("work/sub/leaf")

	if got, want := res.ConfigDir, filepath.Join(tmpHome, ".codex-work-sub"); got != want {
		t.Errorf("config_dir=%q want %q", got, want)
	}
	if got, want := res.ConfigDirSource, "group:work/sub"; got != want {
		t.Errorf("config_dir source=%q want %q", got, want)
	}
	if got, want := res.Command, "codex-work"; got != want {
		t.Errorf("command=%q want %q", got, want)
	}
	if got, want := res.CommandSource, "group:work"; got != want {
		t.Errorf("command source=%q want %q", got, want)
	}
	if got, want := res.Skills, []string{"store/base", "store/shared", "store/extra"}; !reflect.DeepEqual(got, want) {
		t.Errorf("skills=%v want %v", got, want)
	}
	if got, want := res.MCPs, []string{"memory", "exa"}; !reflect.DeepEqual(got, want) {
		t.Errorf("mcps=%v want %v", got, want)
	}
}

func TestResolveGroupCodexHomeSkillsInheritsHomeOwnerSkills(t *testing.T) {
	home := withIsolatedHomeAndConfig(t, `
[groups."work".codex]
config_dir = "~/.codex-work"
skills = ["store/base"]

[groups."work/api".codex]
`)

	codexHome, skills, err := ResolveGroupCodexHomeSkills("work/api")
	if err != nil {
		t.Fatalf("resolve home skills: %v", err)
	}
	if got, want := codexHome, filepath.Join(home, ".codex-work"); got != want {
		t.Fatalf("codex_home=%q want %q", got, want)
	}
	if got, want := skills, []string{"store/base"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("skills=%v want %v", got, want)
	}
}

func TestResolveGroupCodexHomeSkillsRejectsChildOnlySkillsInInheritedHome(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[groups."work".codex]
config_dir = "~/.codex-work"
skills = ["store/base"]

[groups."work/api".codex]
skills = ["store/api"]
`)

	_, _, err := ResolveGroupCodexHomeSkills("work/api")
	if err == nil || !strings.Contains(err.Error(), "config_dir") {
		t.Fatalf("expected child config_dir error, got %v", err)
	}
}

func TestResolveGroupCodexHomeSkillsRejectsDivergentExplicitSharedHome(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[groups."alpha".codex]
config_dir = "~/.codex-shared"
skills = ["store/alpha"]

[groups."beta".codex]
config_dir = "~/.codex-shared"
skills = ["store/beta"]
`)

	_, _, err := ResolveGroupCodexHomeSkills("alpha")
	if err == nil || !strings.Contains(err.Error(), "beta") {
		t.Fatalf("expected divergent shared-home error naming beta, got %v", err)
	}
}

func TestResolveGroupCodexReportsUnsafeSharedHomeSkills(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[groups."work".codex]
config_dir = "~/.codex-work"
skills = ["store/base"]

[groups."work/api".codex]
skills = ["store/api"]
`)

	res := ResolveGroupCodex("work/api")
	if !strings.Contains(res.ConfigError, "config_dir") {
		t.Fatalf("config_error=%q, expected shared-home skill guidance", res.ConfigError)
	}
	if len(res.Skills) != 0 {
		t.Fatalf("unsafe skills exposed as resolved: %v", res.Skills)
	}
}

func TestResolveInstanceCodexHomeSkillsRejectsCommandHomeOverride(t *testing.T) {
	home := withIsolatedHomeAndConfig(t, `
[groups."work".codex]
config_dir = "~/.codex-work"
command = "CODEX_HOME=~/.codex-other codex"
skills = ["store/base"]
`)
	inst := NewInstanceWithGroupAndTool("work", filepath.Join(home, "project"), "work", "codex")

	_, _, err := ResolveInstanceCodexHomeSkills(inst)
	if err == nil || !strings.Contains(err.Error(), "CODEX_HOME") {
		t.Fatalf("expected command home mismatch, got %v", err)
	}
}

func TestPrepareCommandRejectsDivergentSharedCodexHomeSkills(t *testing.T) {
	home := withIsolatedHomeAndConfig(t, `
[groups."alpha".codex]
config_dir = "~/.codex-shared"
skills = ["store/alpha"]

[groups."beta".codex]
config_dir = "~/.codex-shared"
skills = ["store/beta"]
`)
	inst := NewInstanceWithGroupAndTool("beta", filepath.Join(home, "project"), "beta", "codex")

	if _, _, err := inst.prepareCommand("codex"); err == nil || !strings.Contains(err.Error(), "shared config_dir") {
		t.Fatalf("expected unsafe shared-home launch rejection, got %v", err)
	}
}

func TestBuildCodexCommand_UsesGroupCodexConfigDirAsCodexHome(t *testing.T) {
	tmpHome := withIsolatedHomeAndConfig(t, `
[codex]
config_dir = "~/.codex-global"

[groups."work".codex]
config_dir = "~/.codex-work"
`)
	t.Setenv("CODEX_HOME", "")

	inst := NewInstanceWithGroupAndTool("work-codex", "/tmp/work-codex", "work/sub", "codex")
	command := inst.buildCodexCommand(inst.Command)
	want := "CODEX_HOME=" + filepath.Join(tmpHome, ".codex-work")
	if !strings.Contains(command, want) {
		t.Fatalf("expected command to include %q, got %q", want, command)
	}
}

func TestGroupCodexEnvFileOverridesGlobal(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[codex]
env_file = "~/.agent-deck/global.env"

[groups."work".codex]
env_file = "~/.agent-deck/work.env"
`)

	inst := NewInstanceWithGroupAndTool("work-codex", "/tmp/work-codex", "work/sub", "codex")
	if got, want := inst.getToolEnvFile(), "~/.agent-deck/work.env"; got != want {
		t.Errorf("Codex env_file=%q want %q", got, want)
	}
}

func TestSyncGroupCodexPluginsUsesGroupHome(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, "codex-work")
	record := filepath.Join(home, "plugin-invocation.txt")
	fakeCodex := filepath.Join(home, "codex")
	script := "#!/bin/sh\nprintf '%s\\n' \"$CODEX_HOME $*\" >> " + record + "\n"
	if err := os.WriteFile(fakeCodex, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("AGENTDECK_PROFILE", "")
	t.Setenv("CODEX_HOME", "")
	t.Cleanup(ClearUserConfigCache)

	if err := os.MkdirAll(filepath.Join(home, ".agent-deck"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := "[groups.\"work\".codex]\nconfig_dir = \"" + codexHome + "\"\ncommand = \"" + fakeCodex + "\"\nmarketplaces = [\"parent-marketplace\"]\nplugins = [\"agent-deck@team\"]\n\n[groups.\"work/sub\".codex]\nmarketplaces = [\"child-marketplace\"]\nplugins = [\"frontend-design@official\"]\n"
	if err := os.WriteFile(filepath.Join(home, ".agent-deck", "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()

	if err := SyncGroupCodexPlugins("work/sub"); err != nil {
		t.Fatalf("sync plugins: %v", err)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("read invocation: %v", err)
	}
	if got, want := string(data), []string{
		codexHome + " plugin marketplace add parent-marketplace --json",
		codexHome + " plugin marketplace add child-marketplace --json",
		codexHome + " plugin add agent-deck@team --json",
		codexHome + " plugin add frontend-design@official --json",
	}; !reflect.DeepEqual(strings.FieldsFunc(strings.TrimSpace(got), func(r rune) bool { return r == '\n' }), want) {
		t.Errorf("sync invocations=%q want %q", got, want)
	}
	if info, err := os.Stat(codexHome); err != nil || !info.IsDir() {
		t.Errorf("Codex home was not created: info=%v err=%v", info, err)
	}
}
