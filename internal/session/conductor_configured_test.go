package session

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestApplyConductorClaudeConfig_WritesStanza verifies the configured-mode
// inputs land in the [conductors.<name>.claude] block: role/model/effort as
// env keys (matching the live harness-advisor/lilu format) and plugins as the
// declarative loadout list.
func TestApplyConductorClaudeConfig_WritesStanza(t *testing.T) {
	cfg := &UserConfig{}
	applyConductorClaudeConfig(cfg, "ryan", ConductorClaudeConfigInput{
		Role:    "ryan",
		Model:   "claude-opus-4-6",
		Effort:  "xhigh",
		Plugins: []string{"berg-store/loom", "berg-store/memsearch"},
	})

	got := cfg.Conductors["ryan"].Claude
	wantEnv := map[string]string{
		"AGENT_ROLE":               "ryan",
		"ANTHROPIC_MODEL":          "claude-opus-4-6",
		"CLAUDE_CODE_EFFORT_LEVEL": "xhigh",
	}
	if !reflect.DeepEqual(got.Env, wantEnv) {
		t.Errorf("env = %#v, want %#v", got.Env, wantEnv)
	}
	wantPlugins := []string{"berg-store/loom", "berg-store/memsearch"}
	if !reflect.DeepEqual(got.Plugins, wantPlugins) {
		t.Errorf("plugins = %#v, want %#v", got.Plugins, wantPlugins)
	}
}

// TestApplyConductorClaudeConfig_OptionalOmitted verifies optional model/effort
// are not written when empty (so a stanza stays minimal — the harness-advisor
// shape carries only AGENT_ROLE).
func TestApplyConductorClaudeConfig_OptionalOmitted(t *testing.T) {
	cfg := &UserConfig{}
	applyConductorClaudeConfig(cfg, "ha", ConductorClaudeConfigInput{
		Role:    "harness-advisor",
		Plugins: []string{"berg-store/loom"},
	})
	env := cfg.Conductors["ha"].Claude.Env
	if _, ok := env["ANTHROPIC_MODEL"]; ok {
		t.Errorf("ANTHROPIC_MODEL should be absent when --model omitted, got env=%#v", env)
	}
	if _, ok := env["CLAUDE_CODE_EFFORT_LEVEL"]; ok {
		t.Errorf("CLAUDE_CODE_EFFORT_LEVEL should be absent when --effort omitted, got env=%#v", env)
	}
	if env["AGENT_ROLE"] != "harness-advisor" {
		t.Errorf("AGENT_ROLE = %q, want harness-advisor", env["AGENT_ROLE"])
	}
}

// TestApplyConductorClaudeConfig_Idempotent verifies applying the same inputs
// twice yields identical config — managed keys are set, not appended, so
// nothing accumulates.
func TestApplyConductorClaudeConfig_Idempotent(t *testing.T) {
	in := ConductorClaudeConfigInput{
		Role:    "ryan",
		Model:   "claude-opus-4-6",
		Plugins: []string{"berg-store/loom", "berg-store/memsearch"},
	}
	a := &UserConfig{}
	applyConductorClaudeConfig(a, "ryan", in)

	b := &UserConfig{}
	applyConductorClaudeConfig(b, "ryan", in)
	applyConductorClaudeConfig(b, "ryan", in) // second application must be a no-op

	if !reflect.DeepEqual(a.Conductors, b.Conductors) {
		t.Errorf("not idempotent:\n once = %#v\n twice = %#v", a.Conductors, b.Conductors)
	}
	if n := len(b.Conductors["ryan"].Claude.Plugins); n != 2 {
		t.Errorf("plugins accumulated: got %d entries, want 2", n)
	}
}

// TestApplyConductorClaudeConfig_NoClobber verifies the apply preserves other
// conductors and any user-added env keys on the target conductor (update
// in-place, never clobber unrelated config).
func TestApplyConductorClaudeConfig_NoClobber(t *testing.T) {
	cfg := &UserConfig{
		Conductors: map[string]ConductorOverrides{
			"other": {Claude: ConductorClaudeSettings{Env: map[string]string{"AGENT_ROLE": "other"}}},
			"ryan": {Claude: ConductorClaudeSettings{
				ConfigDir: "~/custom-claude",
				Env:       map[string]string{"AGENT_ROLE": "stale", "MY_CUSTOM": "keepme"},
			}},
		},
	}
	applyConductorClaudeConfig(cfg, "ryan", ConductorClaudeConfigInput{
		Role:    "ryan",
		Plugins: []string{"berg-store/loom"},
	})

	// Other conductor untouched.
	if got := cfg.Conductors["other"].Claude.Env["AGENT_ROLE"]; got != "other" {
		t.Errorf("other conductor clobbered: AGENT_ROLE=%q, want other", got)
	}
	ryan := cfg.Conductors["ryan"].Claude
	// Managed key updated.
	if ryan.Env["AGENT_ROLE"] != "ryan" {
		t.Errorf("AGENT_ROLE not updated: %q", ryan.Env["AGENT_ROLE"])
	}
	// User-added env key preserved.
	if ryan.Env["MY_CUSTOM"] != "keepme" {
		t.Errorf("user env key MY_CUSTOM clobbered: %q", ryan.Env["MY_CUSTOM"])
	}
	// Unrelated field on the same block preserved.
	if ryan.ConfigDir != "~/custom-claude" {
		t.Errorf("config_dir clobbered: %q", ryan.ConfigDir)
	}
}

// TestWriteConductorClaudeConfig_RoundTrip exercises the full load → apply →
// save → reload path against a hermetic temp config, and confirms a second
// identical write leaves the file byte-stable (idempotent on disk).
func TestWriteConductorClaudeConfig_RoundTrip(t *testing.T) {
	setupConductorTest(t)

	in := ConductorClaudeConfigInput{
		Role:    "ryan",
		Model:   "claude-opus-4-6",
		Effort:  "xhigh",
		Plugins: []string{"berg-store/loom", "berg-store/memsearch"},
	}
	if err := WriteConductorClaudeConfig("ryan", in); err != nil {
		t.Fatalf("first write: %v", err)
	}

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := cfg.GetConductorClaudeEnv("ryan")["AGENT_ROLE"]; got != "ryan" {
		t.Errorf("reloaded AGENT_ROLE = %q, want ryan", got)
	}
	if got := cfg.GetConductorClaudeEnv("ryan")["ANTHROPIC_MODEL"]; got != "claude-opus-4-6" {
		t.Errorf("reloaded ANTHROPIC_MODEL = %q", got)
	}
	if got := cfg.GetConductorClaudePlugins("ryan"); !reflect.DeepEqual(got, in.Plugins) {
		t.Errorf("reloaded plugins = %#v, want %#v", got, in.Plugins)
	}

	// Idempotent on disk: a second identical write produces identical bytes.
	path, _ := GetUserConfigPath()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if err := WriteConductorClaudeConfig("ryan", in); err != nil {
		t.Fatalf("second write: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read config: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("second identical write changed the file:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
}

// TestMaybeInstallSharedConductorInstructions_Skip verifies the friction fix:
// when skip is true the shared base CLAUDE.md is NOT (re-)created; when false
// it is, matching legacy behavior.
func TestMaybeInstallSharedConductorInstructions_Skip(t *testing.T) {
	setupConductorTest(t)

	dir, err := ConductorDir()
	if err != nil {
		t.Fatalf("ConductorDir: %v", err)
	}
	claudeMD := filepath.Join(dir, "CLAUDE.md")

	// skip=true → file must not be created.
	installed, err := MaybeInstallSharedConductorInstructions(ConductorAgentClaude, "", true)
	if err != nil {
		t.Fatalf("skip install: %v", err)
	}
	if installed {
		t.Error("installed=true when skip requested")
	}
	if _, err := os.Stat(claudeMD); !os.IsNotExist(err) {
		t.Errorf("shared CLAUDE.md created despite skip (stat err=%v)", err)
	}

	// skip=false → legacy behavior, file is written.
	installed, err = MaybeInstallSharedConductorInstructions(ConductorAgentClaude, "", false)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !installed {
		t.Error("installed=false when install requested")
	}
	if _, err := os.Stat(claudeMD); err != nil {
		t.Errorf("shared CLAUDE.md not created when install requested: %v", err)
	}
}
