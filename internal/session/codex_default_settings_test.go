package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestCodexDefaultSettingsParseFromRootConfig(t *testing.T) {
	const config = `
[codex]
default_model = "gpt-5.6"
default_reasoning_effort = "high"
`

	var decoded UserConfig
	metadata, err := toml.Decode(config, &decoded)
	if err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) != 0 {
		t.Fatalf("root Codex defaults must be recognized, undecoded keys: %v", undecoded)
	}
}

func TestCodexDefaultSettingsApplyToFreshSessionCommand(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[codex]
default_model = "gpt-5.6"
default_reasoning_effort = "high"
`)

	inst := NewInstanceWithTool("default-codex", t.TempDir(), "codex")
	command := inst.buildCodexCommand("codex")
	if !strings.Contains(command, "--model gpt-5.6") {
		t.Fatalf("Codex command omitted [codex].default_model:\n%s", command)
	}
	if !strings.Contains(command, "--config model_reasoning_effort=high") {
		t.Fatalf("Codex command omitted [codex].default_reasoning_effort:\n%s", command)
	}
}

func TestCodexGroupSettingsOverrideRootDefaults(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[codex]
default_model = "gpt-5.6"
default_reasoning_effort = "high"

[groups.work.codex]
model = "gpt-5.6-terra"
reasoning_effort = "medium"
`)

	inst := NewInstanceWithGroupAndTool("group-codex", t.TempDir(), "work", "codex")
	command := inst.buildCodexCommand("codex")
	if !strings.Contains(command, "--model gpt-5.6-terra") || strings.Contains(command, "--model gpt-5.6 ") {
		t.Fatalf("group Codex model must override root default:\n%s", command)
	}
	if !strings.Contains(command, "--config model_reasoning_effort=medium") || strings.Contains(command, "model_reasoning_effort=high") {
		t.Fatalf("group Codex reasoning effort must override root default:\n%s", command)
	}
}

func TestCodexExtraArgModelOverridesRootDefault(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[codex]
default_model = "gpt-5.6"
`)

	inst := NewInstanceWithTool("extra-arg-codex", t.TempDir(), "codex")
	inst.ExtraArgs = []string{"--model", "manual-model"}
	command := inst.buildCodexCommand("codex")
	if strings.Contains(command, "gpt-5.6") || strings.Count(command, "--model") != 1 || !strings.Contains(command, "--model manual-model") {
		t.Fatalf("explicit --model extra arg must replace the root default:\n%s", command)
	}
}

func TestConfiguredCodexLoadoutProjectsRootDefaultsWithoutClobberingRuntimeState(t *testing.T) {
	home := t.TempDir()
	withIsolatedHomeAndConfig(t, fmt.Sprintf(`
[codex]
default_model = "gpt-5.6"
default_reasoning_effort = "high"

[groups.work.codex]
config_dir = %q
`, home))
	configPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(configPath, []byte(`[projects."/tmp/work"]
trust_level = "trusted"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	inst := NewInstanceWithGroupAndTool("projected-codex", t.TempDir(), "work", "codex")
	if warnings := ApplyConfiguredLoadout(inst); len(warnings) != 0 {
		t.Fatalf("loadout warnings: %v", warnings)
	}

	var got map[string]any
	if _, err := toml.DecodeFile(configPath, &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "gpt-5.6" {
		t.Fatalf("projected model = %#v, want gpt-5.6", got["model"])
	}
	if got["model_reasoning_effort"] != "high" {
		t.Fatalf("projected reasoning effort = %#v, want high", got["model_reasoning_effort"])
	}
	if _, ok := got["projects"]; !ok {
		t.Fatalf("Codex project trust state was removed: %#v", got)
	}
}

func TestConfiguredCodexLoadoutRemovesStaleProjectedDefaults(t *testing.T) {
	home := t.TempDir()
	config := func(defaults string) string {
		return fmt.Sprintf("%s\n[groups.work.codex]\nconfig_dir = %q\n", defaults, home)
	}
	root := withIsolatedHomeAndConfig(t, config(`[codex]
default_model = "gpt-5.6"
default_reasoning_effort = "high"`))

	inst := NewInstanceWithGroupAndTool("stale-projected-codex", t.TempDir(), "work", "codex")
	if warnings := ApplyConfiguredLoadout(inst); len(warnings) != 0 {
		t.Fatalf("initial loadout warnings: %v", warnings)
	}

	configPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(filepath.Join(root, ".agent-deck", "config.toml"), []byte(config("")), 0o600); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()
	if warnings := ApplyConfiguredLoadout(inst); len(warnings) != 0 {
		t.Fatalf("reconciled loadout warnings: %v", warnings)
	}

	var got map[string]any
	if _, err := toml.DecodeFile(configPath, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["model"]; ok {
		t.Fatalf("stale projected model remained after source removal: %#v", got["model"])
	}
	if _, ok := got["model_reasoning_effort"]; ok {
		t.Fatalf("stale projected reasoning effort remained after source removal: %#v", got["model_reasoning_effort"])
	}
}

func TestResolvedCodexGroupSettingsReportModelAndReasoningEffort(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[groups.work.codex]
model = "gpt-5.6-terra"
reasoning_effort = "medium"
`)

	resolved := ResolveGroupCodex("work")
	if resolved.Model != "gpt-5.6-terra" || resolved.ModelSource != "group:work" {
		t.Fatalf("resolved model = %q from %q, want gpt-5.6-terra from group:work", resolved.Model, resolved.ModelSource)
	}
	if resolved.ReasoningEffort != "medium" || resolved.ReasoningSource != "group:work" {
		t.Fatalf("resolved reasoning effort = %q from %q, want medium from group:work", resolved.ReasoningEffort, resolved.ReasoningSource)
	}
}

func TestConfiguredCodexLoadoutPreservesChangedRuntimeValueWhenSourceIsRemoved(t *testing.T) {
	home := t.TempDir()
	config := func(defaults string) string {
		return fmt.Sprintf("%s\n[groups.work.codex]\nconfig_dir = %q\n", defaults, home)
	}
	root := withIsolatedHomeAndConfig(t, config(`[codex]
default_model = "gpt-5.6"`))
	inst := NewInstanceWithGroupAndTool("changed-codex", t.TempDir(), "work", "codex")
	if warnings := ApplyConfiguredLoadout(inst); len(warnings) != 0 {
		t.Fatalf("initial loadout warnings: %v", warnings)
	}

	configPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(configPath, []byte("model = \"manual-model\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".agent-deck", "config.toml"), []byte(config("")), 0o600); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()
	if warnings := ApplyConfiguredLoadout(inst); len(warnings) != 0 {
		t.Fatalf("reconciled loadout warnings: %v", warnings)
	}

	var got map[string]any
	if _, err := toml.DecodeFile(configPath, &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "manual-model" {
		t.Fatalf("changed runtime model was removed or overwritten: %#v", got["model"])
	}
}
