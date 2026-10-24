package session

import (
	"strings"
	"testing"
)

// evilLaunchValue is a model/agent name that breaks out of a bare splice
// (`;`) and runs a command substitution. Models and agents reach the launch
// command from stored session options, the web API (ApplyLaunchModel),
// `session set model`, a tool's own files (Gemini's chat model) and
// config.toml defaults, so the builders must quote them like session ids.
const evilLaunchValue = "x; touch PWNED; $(touch PWNED)"

// setInjectionUserConfig installs cfg as the cached user config for the
// isolated HOME set up by sessionIDInjectionEnv (no config.toml exists there,
// so the cache is used as-is).
func setInjectionUserConfig(t *testing.T, cfg *UserConfig) {
	t.Helper()
	userConfigCacheMu.Lock()
	userConfigCache = cfg
	userConfigCacheMu.Unlock()
}

func stubOpenCodeV1(t *testing.T) {
	t.Helper()
	orig := probeOpenCodeMajorVersion
	probeOpenCodeMajorVersion = func(*Instance) (int, bool) { return 1, true }
	t.Cleanup(func() { probeOpenCodeMajorVersion = orig })
}

// Gemini resume with a model read back from Gemini's chat file / web API.
func TestLaunchValueQuoting_GeminiModelResume(t *testing.T) {
	project := sessionIDInjectionEnv(t)
	inst := NewInstanceWithTool("val-inject", project, "gemini")
	inst.GeminiSessionID = ordinarySessionID
	inst.GeminiModel = evilLaunchValue
	cmdStr := inst.buildGeminiCommand("gemini")
	assertArgFollows(t, runWithFakeTool(t, "gemini", cmdStr), "--model", evilLaunchValue, cmdStr)
}

// Gemini fresh start with a per-session model.
func TestLaunchValueQuoting_GeminiModelFresh(t *testing.T) {
	project := sessionIDInjectionEnv(t)
	inst := NewInstanceWithTool("val-inject", project, "gemini")
	inst.GeminiModel = evilLaunchValue
	cmdStr := inst.buildGeminiCommand("gemini")
	assertArgFollows(t, runWithFakeTool(t, "gemini", cmdStr), "--model", evilLaunchValue, cmdStr)
}

// Gemini fresh start with [gemini].default_model from config.
func TestLaunchValueQuoting_GeminiDefaultModel(t *testing.T) {
	project := sessionIDInjectionEnv(t)
	setInjectionUserConfig(t, &UserConfig{Gemini: GeminiSettings{DefaultModel: evilLaunchValue}})
	inst := NewInstanceWithTool("val-inject", project, "gemini")
	cmdStr := inst.buildGeminiCommand("gemini")
	assertArgFollows(t, runWithFakeTool(t, "gemini", cmdStr), "--model", evilLaunchValue, cmdStr)
}

// OpenCode v1 with stored per-session model and agent (web API, `session set
// model`, `--agent`).
func TestLaunchValueQuoting_OpenCodeV1Options(t *testing.T) {
	project := sessionIDInjectionEnv(t)
	stubOpenCodeV1(t)
	inst := NewInstanceWithTool("val-inject", project, "opencode")
	if err := inst.SetOpenCodeOptions(&OpenCodeOptions{Model: evilLaunchValue, Agent: evilLaunchValue}); err != nil {
		t.Fatal(err)
	}
	cmdStr := inst.buildOpenCodeCommand("opencode")
	args := runWithFakeTool(t, "opencode", cmdStr)
	assertArgFollows(t, args, "-m", evilLaunchValue, cmdStr)
	assertArgFollows(t, args, "--agent", evilLaunchValue, cmdStr)
}

// OpenCode v1 resume keeps the flags quoted after the quoted -s id.
func TestLaunchValueQuoting_OpenCodeV1Resume(t *testing.T) {
	project := sessionIDInjectionEnv(t)
	stubOpenCodeV1(t)
	inst := NewInstanceWithTool("val-inject", project, "opencode")
	inst.OpenCodeSessionID = "ses_abc123"
	if err := inst.SetOpenCodeOptions(&OpenCodeOptions{Model: evilLaunchValue, Agent: evilLaunchValue}); err != nil {
		t.Fatal(err)
	}
	cmdStr := inst.buildOpenCodeCommand("opencode")
	args := runWithFakeTool(t, "opencode", cmdStr)
	assertArgFollows(t, args, "-m", evilLaunchValue, cmdStr)
	assertArgFollows(t, args, "--agent", evilLaunchValue, cmdStr)
}

// OpenCode v1 with [opencode].default_model / default_agent from config.
func TestLaunchValueQuoting_OpenCodeV1DefaultModelAgent(t *testing.T) {
	project := sessionIDInjectionEnv(t)
	stubOpenCodeV1(t)
	setInjectionUserConfig(t, &UserConfig{OpenCode: OpenCodeSettings{
		DefaultModel: evilLaunchValue, DefaultAgent: evilLaunchValue,
	}})
	inst := NewInstanceWithTool("val-inject", project, "opencode")
	cmdStr := inst.buildOpenCodeCommand("opencode")
	args := runWithFakeTool(t, "opencode", cmdStr)
	assertArgFollows(t, args, "-m", evilLaunchValue, cmdStr)
	assertArgFollows(t, args, "--agent", evilLaunchValue, cmdStr)
}

// Copilot with a stored per-session model.
func TestLaunchValueQuoting_CopilotModel(t *testing.T) {
	sessionIDInjectionEnv(t)
	inst := &Instance{Tool: "copilot", Command: "copilot", CopilotModel: evilLaunchValue}
	cmdStr := buildCopilotCommand(inst)
	assertArgFollows(t, runWithFakeTool(t, "copilot", cmdStr), "--model", evilLaunchValue, cmdStr)
}

// Copilot with [copilot].default_model from config.
func TestLaunchValueQuoting_CopilotDefaultModel(t *testing.T) {
	sessionIDInjectionEnv(t)
	setInjectionUserConfig(t, &UserConfig{Copilot: CopilotSettings{DefaultModel: evilLaunchValue}})
	inst := &Instance{Tool: "copilot", Command: "copilot"}
	cmdStr := buildCopilotCommand(inst)
	assertArgFollows(t, runWithFakeTool(t, "copilot", cmdStr), "--model", evilLaunchValue, cmdStr)
}

// TestLaunchValueQuoting_OrdinaryValuesUnchanged pins that the hardening
// leaves the command text for ordinary model and agent names byte-identical.
func TestLaunchValueQuoting_OrdinaryValuesUnchanged(t *testing.T) {
	cases := []struct {
		name  string
		build func(t *testing.T) string
		want  string
	}{
		{"gemini resume model", func(t *testing.T) string {
			inst := NewInstanceWithTool("val-plain", sessionIDInjectionEnv(t), "gemini")
			inst.GeminiSessionID = ordinarySessionID
			inst.GeminiModel = "gemini-2.5-pro"
			return inst.buildGeminiCommand("gemini")
		}, "gemini --resume " + ordinarySessionID + " --model gemini-2.5-pro"},
		{"gemini default model", func(t *testing.T) string {
			project := sessionIDInjectionEnv(t)
			setInjectionUserConfig(t, &UserConfig{Gemini: GeminiSettings{DefaultModel: "gemini-2.5-flash"}})
			return NewInstanceWithTool("val-plain", project, "gemini").buildGeminiCommand("gemini")
		}, "gemini --model gemini-2.5-flash"},
		{"opencode v1 model agent", func(t *testing.T) string {
			project := sessionIDInjectionEnv(t)
			stubOpenCodeV1(t)
			inst := NewInstanceWithTool("val-plain", project, "opencode")
			_ = inst.SetOpenCodeOptions(&OpenCodeOptions{Model: "anthropic/claude-sonnet-4", Agent: "build"})
			return inst.buildOpenCodeCommand("opencode")
		}, "opencode -m anthropic/claude-sonnet-4 --agent build"},
		{"opencode v1 defaults", func(t *testing.T) string {
			project := sessionIDInjectionEnv(t)
			stubOpenCodeV1(t)
			setInjectionUserConfig(t, &UserConfig{OpenCode: OpenCodeSettings{DefaultModel: "openai/gpt-5.1", DefaultAgent: "plan"}})
			return NewInstanceWithTool("val-plain", project, "opencode").buildOpenCodeCommand("opencode")
		}, "opencode -m openai/gpt-5.1 --agent plan"},
		{"copilot model", func(t *testing.T) string {
			sessionIDInjectionEnv(t)
			return buildCopilotCommand(&Instance{Tool: "copilot", Command: "copilot", CopilotModel: "claude-sonnet-4.5"})
		}, "copilot --model claude-sonnet-4.5"},
		{"copilot default model", func(t *testing.T) string {
			sessionIDInjectionEnv(t)
			setInjectionUserConfig(t, &UserConfig{Copilot: CopilotSettings{DefaultModel: "claude-opus-4.6"}})
			return buildCopilotCommand(&Instance{Tool: "copilot", Command: "copilot"})
		}, "copilot --model claude-opus-4.6"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.build(t)
			if !strings.Contains(got, tc.want) {
				t.Errorf("command = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}
