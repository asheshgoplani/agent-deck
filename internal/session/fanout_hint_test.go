package session

import (
	"os"
	"strings"
	"testing"
)

// wantFanoutHint is the exact line the brief asks every launched worker to
// see. Spelled out here (not via FanoutHint) so a silent rewording of the
// constant fails this test.
const wantFanoutHint = "Use a dynamic Workflow (fan-out agents) for every set of independent steps; serial only what must be ordered."

func TestFanoutHint_ExactWording(t *testing.T) {
	if FanoutHint != wantFanoutHint {
		t.Fatalf("FanoutHint = %q, want %q", FanoutHint, wantFanoutHint)
	}
}

func TestLaunchSettings_GetFanoutHintDefaultsTrue(t *testing.T) {
	var nilSettings *LaunchSettings
	if !nilSettings.GetFanoutHint() {
		t.Error("nil settings must default to true")
	}
	if !(&LaunchSettings{}).GetFanoutHint() {
		t.Error("unset must default to true")
	}
	off := false
	if (&LaunchSettings{FanoutHint: &off}).GetFanoutHint() {
		t.Error("explicit false must be honoured")
	}
}

func TestIdentityPrompt_CarriesFanoutHintByDefault(t *testing.T) {
	identityTestEnv(t)
	inst := identityTestInstance("claude")
	for level, got := range map[string]string{
		ContextLevelFull:   inst.BuildContextPromptForLevel(ContextLevelFull),
		ContextLevelPrimer: inst.BuildContextPromptForLevel(ContextLevelPrimer),
	} {
		if !strings.Contains(got, wantFanoutHint+"\n") {
			t.Errorf("%s block must carry the fan-out hint by default:\n%s", level, got)
		}
	}
}

func TestIdentityPrompt_OmitsFanoutHintWhenDisabled(t *testing.T) {
	identityTestEnv(t)
	off := false
	restore := resetUserConfigCache(t, &UserConfig{Launch: LaunchSettings{FanoutHint: &off}})
	defer restore()
	inst := identityTestInstance("claude")
	if !inst.identityInjectionEnabled() {
		t.Fatal("fanout_hint=false must not disable identity injection itself")
	}
	for _, level := range []string{ContextLevelFull, ContextLevelPrimer} {
		got := inst.BuildContextPromptForLevel(level)
		if strings.Contains(got, "fan-out") {
			t.Errorf("%s block must not carry the fan-out hint with fanout_hint=false:\n%s", level, got)
		}
		if !strings.Contains(got, "- session id: abc12345-1700000000") {
			t.Errorf("%s block lost its identity with fanout_hint=false:\n%s", level, got)
		}
	}
}

// --no-identity writes no identity file and passes no harness flag, so the
// hint (which lives only in that file) never reaches the model.
func TestIdentityPrompt_NoIdentityCarriesNoFanoutHint(t *testing.T) {
	identityTestEnv(t)
	inst := identityTestInstance("claude")
	inst.IdentityInjectionDisabled = true
	if _, _, ok := inst.ensureIdentityFile(); ok {
		t.Fatal("--no-identity must not write an identity file")
	}
	if got := inst.claudeIdentityFlag(); got != "" {
		t.Errorf("--no-identity claude flag = %q, want empty", got)
	}
	if got := inst.identityEnvExport(); got != "" {
		t.Errorf("--no-identity env export = %q, want empty", got)
	}
}

// The identity file a default spawn actually writes carries the hint.
func TestEnsureIdentityFile_WritesFanoutHint(t *testing.T) {
	identityTestEnv(t)
	inst := identityTestInstance("claude")
	_, file, ok := inst.ensureIdentityFile()
	if !ok {
		t.Fatal("identity file not written")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	data := string(raw)
	if !strings.Contains(data, wantFanoutHint) {
		t.Errorf("identity file %s missing the fan-out hint:\n%s", file, data)
	}
}

// "dynamic Workflow" is a Claude Code tool: only Claude-family sessions get
// the line. Other harnesses could read "fan-out agents" as "launch an
// agent-deck child per step", which compounds at every level.
func TestIdentityPrompt_FanoutHintOnlyForClaudeFamily(t *testing.T) {
	identityTestEnv(t)
	for _, tc := range []struct {
		tool string
		want bool
	}{{"claude", true}, {"codex", false}, {"gemini", false}, {"pi", false}, {"omp", false}} {
		inst := identityTestInstance(tc.tool)
		for _, level := range []string{ContextLevelFull, ContextLevelPrimer} {
			got := inst.BuildContextPromptForLevel(level)
			if strings.Contains(got, wantFanoutHint) != tc.want {
				t.Errorf("%s %s block carries the fan-out hint = %v, want %v:\n%s", tc.tool, level, !tc.want, tc.want, got)
			}
			if !tc.want && strings.Contains(got, "fan-out") {
				t.Errorf("%s %s block mentions fan-out:\n%s", tc.tool, level, got)
			}
		}
	}
}

// The primer carries guidance once it has the hint, so it ends with the
// same precedence note as the full block, right after the hint.
func TestPrimerPrompt_FanoutHintComesWithPrecedenceNote(t *testing.T) {
	identityTestEnv(t)
	const note = "This block only adds context. Instructions from your operator, from project or conductor files (CLAUDE.md, AGENTS.md, GEMINI.md) and from the task you were given take precedence over it.\n"
	got := identityTestInstance("claude").BuildPrimerPrompt()
	if !strings.Contains(got, wantFanoutHint+"\n"+note) {
		t.Errorf("claude primer must follow the hint with the precedence note:\n%s", got)
	}
	if full := identityTestInstance("claude").BuildIdentityPrompt(); !strings.Contains(full, "\n"+note) {
		t.Errorf("full block lost its precedence note:\n%s", full)
	}
	off := false
	restore := resetUserConfigCache(t, &UserConfig{Launch: LaunchSettings{FanoutHint: &off}})
	defer restore()
	if got := identityTestInstance("claude").BuildPrimerPrompt(); strings.Contains(got, note) {
		t.Errorf("primer without the hint must stay as it was:\n%s", got)
	}
}

// config get/set expose the switch and the global context level the
// launch-subagent.sh script reads.
func TestConfigKeys_FanoutHintAndGlobalContextLevel(t *testing.T) {
	get := func(cfg *UserConfig, key string) any {
		k, ok := LookupConfigKey(key)
		if !ok {
			t.Fatalf("%s missing from the schema", key)
		}
		return k.Get(cfg)
	}
	off, no := false, false
	if got := get(&UserConfig{}, "launch.fanout_hint"); got != true {
		t.Errorf("launch.fanout_hint default = %v, want true", got)
	}
	if got := get(&UserConfig{Launch: LaunchSettings{FanoutHint: &off}}, "launch.fanout_hint"); got != false {
		t.Errorf("launch.fanout_hint = %v, want false", got)
	}
	for _, tc := range []struct {
		cfg  *UserConfig
		want string
	}{
		{&UserConfig{}, ContextLevelFull},
		{&UserConfig{Launch: LaunchSettings{ContextLevel: "primer"}}, ContextLevelPrimer},
		{&UserConfig{Launch: LaunchSettings{InjectIdentity: &no}}, ContextLevelNone},
		{&UserConfig{Launch: LaunchSettings{InjectIdentity: &no, ContextLevel: "full"}}, ContextLevelFull},
	} {
		if got := get(tc.cfg, "launch.context_level"); got != tc.want {
			t.Errorf("launch.context_level for %+v = %v, want %s", tc.cfg.Launch, got, tc.want)
		}
	}
}
