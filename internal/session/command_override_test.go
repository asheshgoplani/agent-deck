package session

import (
	"os"
	"strings"
	"testing"

	"al.essio.dev/pkg/shellescape"
)

// Tests for the uniform command/env_file override layer.
// Verifies GetToolCommand, buildCopilotCommand, and getToolEnvFile wiring.

func TestGetToolCommand_NoConfig(t *testing.T) {
	// With no config file on disk, GetToolCommand should return the bare tool name.
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)
	ClearUserConfigCache()
	defer ClearUserConfigCache()

	tools := []string{"claude", "gemini", "opencode", "codex", "copilot", "hermes"}
	for _, tool := range tools {
		got := GetToolCommand(tool)
		if got != tool {
			t.Errorf("GetToolCommand(%q) with no config = %q, want %q", tool, got, tool)
		}
	}
}

func TestGetToolCommand_WithOverride(t *testing.T) {
	cfg := &UserConfig{
		Claude:   ClaudeSettings{Command: "/usr/local/bin/claude-custom"},
		Gemini:   GeminiSettings{Command: "gemini --custom-flag"},
		OpenCode: OpenCodeSettings{Command: "opencode-nightly"},
		Codex:    CodexSettings{Command: "codex --experimental"},
		Copilot:  CopilotSettings{Command: "gh copilot"},
		Hermes:   HermesSettings{Command: "hermes --model gpt-5.5-pro --provider openai"},
	}
	restore := resetUserConfigCache(t, cfg)
	defer restore()

	tests := []struct {
		tool     string
		expected string
	}{
		{"claude", "/usr/local/bin/claude-custom"},
		{"gemini", "gemini --custom-flag"},
		{"opencode", "opencode-nightly"},
		{"codex", "codex --experimental"},
		{"copilot", "gh copilot"},
		{"hermes", "hermes --model gpt-5.5-pro --provider openai"},
	}

	for _, tt := range tests {
		got := GetToolCommand(tt.tool)
		if got != tt.expected {
			t.Errorf("GetToolCommand(%q) = %q, want %q", tt.tool, got, tt.expected)
		}
	}
}

func TestGetToolCommand_EmptyOverrideFallsBack(t *testing.T) {
	// Empty Command fields should fall back to bare tool name.
	cfg := &UserConfig{
		Claude:   ClaudeSettings{Command: ""},
		Gemini:   GeminiSettings{Command: ""},
		OpenCode: OpenCodeSettings{Command: ""},
		Codex:    CodexSettings{Command: ""},
		Copilot:  CopilotSettings{Command: ""},
		Hermes:   HermesSettings{Command: ""},
	}
	restore := resetUserConfigCache(t, cfg)
	defer restore()

	tools := []string{"claude", "gemini", "opencode", "codex", "copilot", "hermes"}
	for _, tool := range tools {
		got := GetToolCommand(tool)
		if got != tool {
			t.Errorf("GetToolCommand(%q) with empty override = %q, want %q", tool, got, tool)
		}
	}
}

func TestGetToolCommand_UnknownTool(t *testing.T) {
	cfg := &UserConfig{}
	restore := resetUserConfigCache(t, cfg)
	defer restore()

	got := GetToolCommand("unknown-tool")
	if got != "unknown-tool" {
		t.Errorf("GetToolCommand(\"unknown-tool\") = %q, want %q", got, "unknown-tool")
	}
}

func TestGetClaudeCommand_DelegatesToGetToolCommand(t *testing.T) {
	cfg := &UserConfig{
		Claude: ClaudeSettings{Command: "claude-wrapper"},
	}
	restore := resetUserConfigCache(t, cfg)
	defer restore()

	got := GetClaudeCommand()
	if got != "claude-wrapper" {
		t.Errorf("GetClaudeCommand() = %q, want %q", got, "claude-wrapper")
	}
}

func TestBuildCopilotCommand_BareNameNoConfig(t *testing.T) {
	cfg := &UserConfig{}
	restore := resetUserConfigCache(t, cfg)
	defer restore()

	inst := &Instance{Tool: "copilot"}
	got := inst.buildCopilotCommand("copilot")
	// Should end with "copilot" (may have env prefix)
	if !strings.HasSuffix(got, "copilot") {
		t.Errorf("buildCopilotCommand(\"copilot\") = %q, want suffix \"copilot\"", got)
	}
}

func TestBuildCopilotCommand_BareNameWithOverride(t *testing.T) {
	cfg := &UserConfig{
		Copilot: CopilotSettings{Command: "gh copilot"},
	}
	restore := resetUserConfigCache(t, cfg)
	defer restore()

	inst := &Instance{Tool: "copilot"}
	got := inst.buildCopilotCommand("copilot")
	if !strings.HasSuffix(got, "gh copilot") {
		t.Errorf("buildCopilotCommand(\"copilot\") with override = %q, want suffix \"gh copilot\"", got)
	}
}

func TestBuildCopilotCommand_CustomCommandPassthrough(t *testing.T) {
	cfg := &UserConfig{
		Copilot: CopilotSettings{Command: "gh copilot"},
	}
	restore := resetUserConfigCache(t, cfg)
	defer restore()

	inst := &Instance{Tool: "copilot"}
	got := inst.buildCopilotCommand("copilot --verbose")
	// Custom command should pass through, NOT use the config override
	if !strings.HasSuffix(got, "copilot --verbose") {
		t.Errorf("buildCopilotCommand(\"copilot --verbose\") = %q, want suffix \"copilot --verbose\"", got)
	}
	if strings.Contains(got, "gh copilot") {
		t.Errorf("buildCopilotCommand should not apply config override for custom commands, got %q", got)
	}
}

func TestBuildCopilotCommand_WrongTool(t *testing.T) {
	inst := &Instance{Tool: "claude"}
	got := inst.buildCopilotCommand("some-command")
	if got != "some-command" {
		t.Errorf("buildCopilotCommand with wrong tool = %q, want %q", got, "some-command")
	}
}

func TestBuildPiCommand_UsesInstanceScopedSessionDir(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	inst := &Instance{ID: "test-instance-id", Tool: "pi"}
	got := inst.buildPiCommand("pi")

	wantSessionDir := "${HOME}/.pi/agent-deck/test-instance-id"
	for _, want := range []string{
		"session_dir=" + wantSessionDir,
		"mkdir -p \"$session_dir\"",
		"AGENTDECK_INSTANCE_ID=test-instance-id",
		"pi --continue --session-dir \"$session_dir\"",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("buildPiCommand() = %q, want to contain %q", got, want)
		}
	}
	if strings.Contains(got, tmpDir) {
		t.Errorf("buildPiCommand() must use target-side $HOME, got host path in %q", got)
	}
}

func TestBuildPiCommand_QuotesInstanceIDPathComponent(t *testing.T) {
	inst := &Instance{ID: "test instance'id", Tool: "pi"}
	got := inst.buildPiCommand("pi")

	wantSessionDir := `${HOME}/.pi/agent-deck/` + shellescape.Quote(inst.ID)
	if !strings.Contains(got, "session_dir="+wantSessionDir) {
		t.Errorf("buildPiCommand() should quote instance ID path component %q, got %q", wantSessionDir, got)
	}
}

func TestBuildPiCommand_WrongTool(t *testing.T) {
	inst := &Instance{Tool: "claude"}
	got := inst.buildPiCommand("some-command")
	if got != "some-command" {
		t.Errorf("buildPiCommand with wrong tool = %q, want %q", got, "some-command")
	}
}

func TestCanRestartPi(t *testing.T) {
	inst := &Instance{Tool: "pi", Status: StatusWaiting}
	if !inst.CanRestart() {
		t.Fatal("Pi sessions should be restartable so Agent Deck can relaunch with --continue")
	}
}

func TestGetToolEnvFile_AllBuiltins(t *testing.T) {
	cfg := &UserConfig{
		Claude:   ClaudeSettings{EnvFile: "/tmp/claude.env"},
		Gemini:   GeminiSettings{EnvFile: "/tmp/gemini.env"},
		OpenCode: OpenCodeSettings{EnvFile: "/tmp/opencode.env"},
		Codex:    CodexSettings{EnvFile: "/tmp/codex.env"},
		Copilot:  CopilotSettings{EnvFile: "/tmp/copilot.env"},
		Hermes:   HermesSettings{EnvFile: "/tmp/hermes.env"},
	}
	restore := resetUserConfigCache(t, cfg)
	defer restore()

	tests := []struct {
		tool     string
		expected string
	}{
		{"claude", "/tmp/claude.env"},
		{"gemini", "/tmp/gemini.env"},
		{"opencode", "/tmp/opencode.env"},
		{"codex", "/tmp/codex.env"},
		{"copilot", "/tmp/copilot.env"},
		{"hermes", "/tmp/hermes.env"},
	}

	for _, tt := range tests {
		inst := &Instance{Tool: tt.tool}
		got := inst.getToolEnvFile()
		if got != tt.expected {
			t.Errorf("getToolEnvFile() for %q = %q, want %q", tt.tool, got, tt.expected)
		}
	}
}

func TestBuildCodexCommand_Passthrough(t *testing.T) {
	cfg := &UserConfig{
		Codex: CodexSettings{Command: "codex-nightly"},
	}
	restore := resetUserConfigCache(t, cfg)
	defer restore()

	inst := &Instance{Tool: "codex"}
	got := inst.buildCodexCommand("codex-custom --flag")
	// Custom command should pass through without flag injection
	if !strings.HasSuffix(got, "codex-custom --flag") {
		t.Errorf("buildCodexCommand passthrough = %q, want suffix \"codex-custom --flag\"", got)
	}
	// Should NOT contain --yolo (passthrough mode)
	if strings.Contains(got, "--yolo") {
		t.Errorf("buildCodexCommand passthrough should not inject --yolo, got %q", got)
	}
}

// TestBuildCodexCommand_PassthroughKeepsAgentdeckEnv asserts that the
// AGENTDECK_INSTANCE_ID / AGENTDECK_TITLE / AGENTDECK_TOOL env injection is
// preserved on the codex custom-command passthrough path. The uniform-command
// rework on #951 originally introduced an early-return that dropped this
// prefix; review flagged it as a behaviour regression (Claude/Codex hook
// subprocesses use AGENTDECK_INSTANCE_ID to find the spawning session), so
// the takeover restores the inline injection ahead of the passthrough
// early-return. Regression-pin so this never silently regresses again.
func TestBuildCodexCommand_PassthroughKeepsAgentdeckEnv(t *testing.T) {
	cfg := &UserConfig{
		Codex: CodexSettings{Command: "codex-nightly"},
	}
	restore := resetUserConfigCache(t, cfg)
	defer restore()

	inst := &Instance{
		ID:    "test-instance-id",
		Title: "test session",
		Tool:  "codex",
	}
	got := inst.buildCodexCommand("codex-custom --flag")

	if !strings.Contains(got, "AGENTDECK_INSTANCE_ID=test-instance-id") {
		t.Errorf("custom-command codex passthrough must include AGENTDECK_INSTANCE_ID, got %q", got)
	}
	if !strings.Contains(got, "AGENTDECK_TOOL=codex") {
		t.Errorf("custom-command codex passthrough must include AGENTDECK_TOOL=codex, got %q", got)
	}
	if !strings.Contains(got, `AGENTDECK_TITLE="test session"`) {
		t.Errorf("custom-command codex passthrough must include AGENTDECK_TITLE, got %q", got)
	}
}

func TestBuildCodexCommand_BareNameUsesOverride(t *testing.T) {
	cfg := &UserConfig{
		Codex: CodexSettings{Command: "codex-nightly", YoloMode: true},
	}
	restore := resetUserConfigCache(t, cfg)
	defer restore()

	inst := &Instance{Tool: "codex"}
	got := inst.buildCodexCommand("codex")
	// Should use the override binary
	if !strings.Contains(got, "codex-nightly") {
		t.Errorf("buildCodexCommand bare name = %q, want to contain \"codex-nightly\"", got)
	}
}

func TestBuildGeminiCommand_UsesOverride(t *testing.T) {
	cfg := &UserConfig{
		Gemini: GeminiSettings{Command: "gemini-nightly"},
	}
	restore := resetUserConfigCache(t, cfg)
	defer restore()

	inst := &Instance{Tool: "gemini"}
	got := inst.buildGeminiCommand("gemini")
	if !strings.Contains(got, "gemini-nightly") {
		t.Errorf("buildGeminiCommand with override = %q, want to contain \"gemini-nightly\"", got)
	}
	if strings.Contains(got, "gemini-nightly-nightly") {
		t.Errorf("buildGeminiCommand doubled the override: %q", got)
	}
}
