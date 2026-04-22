package session

// Issue #556: GitHub Copilot CLI support.
// These tests define the Tool="copilot" contract: options marshalling,
// ToArgs for new/resume, factory from config, and the basic identity gates
// (icon, IsClaudeCompatible, builtin-name filter), plus the buildCopilotCommand
// command-construction surface (yolo, model, autopilot, sub-agent add-dir,
// config-dir prefix, custom command).
//
// Model: https://docs.github.com/en/copilot/concepts/agents/about-copilot-cli
// Binary: `copilot` (npm @github/copilot), interactive REPL with --resume
// picker for prior sessions.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// --- Copilot Options Tests ---

func TestCopilotOptions_ToolName(t *testing.T) {
	opts := &CopilotOptions{}
	if opts.ToolName() != "copilot" {
		t.Errorf("expected ToolName() = 'copilot', got %q", opts.ToolName())
	}
}

func TestCopilotOptions_ToArgs(t *testing.T) {
	tests := []struct {
		name     string
		opts     CopilotOptions
		expected []string
	}{
		{
			name:     "yolo nil (inherit)",
			opts:     CopilotOptions{YoloMode: nil},
			expected: nil,
		},
		{
			name:     "yolo true",
			opts:     CopilotOptions{YoloMode: boolPtr(true)},
			expected: []string{"--yolo"},
		},
		{
			name:     "yolo false",
			opts:     CopilotOptions{YoloMode: boolPtr(false)},
			expected: nil,
		},
		{
			name:     "model set",
			opts:     CopilotOptions{Model: "gpt-5.2"},
			expected: []string{"--model", "gpt-5.2"},
		},
		{
			name:     "yolo + model",
			opts:     CopilotOptions{YoloMode: boolPtr(true), Model: "claude-sonnet-4.5"},
			expected: []string{"--yolo", "--model", "claude-sonnet-4.5"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.opts.ToArgs()
			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("ToArgs() = %v, expected %v", got, tt.expected)
			}
		})
	}
}

func TestNewCopilotOptions_WithConfig(t *testing.T) {
	config := &UserConfig{
		Copilot: CopilotSettings{YoloMode: true, DefaultModel: "gpt-5.2"},
	}
	opts := NewCopilotOptions(config)
	if opts.YoloMode == nil || !*opts.YoloMode {
		t.Error("expected YoloMode=true when config.Copilot.YoloMode=true")
	}
	if opts.Model != "gpt-5.2" {
		t.Errorf("expected Model='gpt-5.2', got %q", opts.Model)
	}

	config2 := &UserConfig{
		Copilot: CopilotSettings{YoloMode: false},
	}
	opts2 := NewCopilotOptions(config2)
	if opts2.YoloMode != nil {
		t.Errorf("expected YoloMode=nil when config.Copilot.YoloMode=false, got %v", *opts2.YoloMode)
	}
}

func TestNewCopilotOptions_NilConfig(t *testing.T) {
	opts := NewCopilotOptions(nil)
	if opts.YoloMode != nil {
		t.Errorf("expected YoloMode=nil when config is nil, got %v", *opts.YoloMode)
	}
	if opts.Model != "" {
		t.Errorf("expected Model='', got %q", opts.Model)
	}
}

func TestCopilotOptions_MarshalUnmarshal(t *testing.T) {
	original := &CopilotOptions{YoloMode: boolPtr(true), Model: "gpt-5.2"}

	data, err := MarshalToolOptions(original)
	if err != nil {
		t.Fatalf("MarshalToolOptions failed: %v", err)
	}

	restored, err := UnmarshalCopilotOptions(data)
	if err != nil {
		t.Fatalf("UnmarshalCopilotOptions failed: %v", err)
	}

	if restored.YoloMode == nil || !*restored.YoloMode {
		t.Error("expected YoloMode=true after roundtrip")
	}
	if restored.Model != "gpt-5.2" {
		t.Errorf("expected Model='gpt-5.2' after roundtrip, got %q", restored.Model)
	}
}

func TestUnmarshalCopilotOptions_EmptyData(t *testing.T) {
	result, err := UnmarshalCopilotOptions(nil)
	if err != nil {
		t.Fatalf("UnmarshalCopilotOptions(nil) failed: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for empty data, got %v", result)
	}
}

func TestUnmarshalCopilotOptions_WrongTool(t *testing.T) {
	claudeOpts := &ClaudeOptions{SkipPermissions: true}
	data, _ := MarshalToolOptions(claudeOpts)

	result, err := UnmarshalCopilotOptions(data)
	if err != nil {
		t.Fatalf("UnmarshalCopilotOptions failed: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for wrong tool, got %v", result)
	}
}

func TestCopilotOptions_RoundTrip_NilYolo(t *testing.T) {
	original := &CopilotOptions{YoloMode: nil, Model: ""}

	data, err := MarshalToolOptions(original)
	if err != nil {
		t.Fatalf("MarshalToolOptions failed: %v", err)
	}

	restored, err := UnmarshalCopilotOptions(data)
	if err != nil {
		t.Fatalf("UnmarshalCopilotOptions failed: %v", err)
	}

	if restored.YoloMode != nil {
		t.Errorf("expected YoloMode=nil after roundtrip, got %v", *restored.YoloMode)
	}
}

func TestCopilotOptions_ToArgsForFork(t *testing.T) {
	opts := &CopilotOptions{
		YoloMode: boolPtr(true),
		Model:    "gpt-5.2",
	}
	got := opts.ToArgsForFork()
	expected := []string{"--yolo", "--model", "gpt-5.2"}
	if !reflect.DeepEqual(got, expected) {
		t.Errorf("ToArgsForFork() = %v, expected %v", got, expected)
	}
}

// --- buildCopilotCommand Tests ---

// copilotCmdTestEnv isolates a test from the user's HOME and user-config
// cache. Returns a cleanup func.
func copilotCmdTestEnv(t *testing.T) func() {
	t.Helper()
	origHome := os.Getenv("HOME")
	origConfigDir := os.Getenv("COPILOT_HOME")
	tmp := t.TempDir()
	os.Setenv("HOME", tmp)
	os.Unsetenv("COPILOT_HOME")
	ClearUserConfigCache()
	return func() {
		os.Setenv("HOME", origHome)
		if origConfigDir != "" {
			os.Setenv("COPILOT_HOME", origConfigDir)
		} else {
			os.Unsetenv("COPILOT_HOME")
		}
		ClearUserConfigCache()
	}
}

// TestBuildCopilotCommand_FreshStart verifies a new session omits --resume/
// --continue and propagates AGENTDECK_* env vars.
func TestBuildCopilotCommand_FreshStart(t *testing.T) {
	defer copilotCmdTestEnv(t)()

	inst := NewInstanceWithTool("test", "/tmp/test", "copilot")
	cmd := inst.buildCopilotCommand("copilot")

	if !strings.Contains(cmd, "copilot") {
		t.Errorf("Command should contain 'copilot', got: %s", cmd)
	}
	if strings.Contains(cmd, "--resume") {
		t.Errorf("Fresh session should NOT contain --resume, got: %s", cmd)
	}
	if strings.Contains(cmd, "--continue") {
		t.Errorf("Fresh session should NOT contain --continue, got: %s", cmd)
	}
	if !strings.Contains(cmd, "AGENTDECK_INSTANCE_ID="+inst.ID) {
		t.Errorf("Should propagate AGENTDECK_INSTANCE_ID, got: %s", cmd)
	}
	if !strings.Contains(cmd, "AGENTDECK_TOOL=copilot") {
		t.Errorf("Should propagate AGENTDECK_TOOL=copilot, got: %s", cmd)
	}
}

// TestBuildCopilotCommand_ResumeWithSessionID verifies that once a session ID
// is detected, restart uses --resume=<id>.
func TestBuildCopilotCommand_ResumeWithSessionID(t *testing.T) {
	defer copilotCmdTestEnv(t)()

	inst := NewInstanceWithTool("test", "/tmp/test", "copilot")
	inst.CopilotSessionID = "abc-123-def"

	cmd := inst.buildCopilotCommand("copilot")
	if !strings.Contains(cmd, "--resume=abc-123-def") {
		t.Errorf("Should use --resume=<id> when CopilotSessionID is set, got: %s", cmd)
	}
}

// TestBuildCopilotCommand_NonCopilotTool verifies the function passes the
// command through unchanged when the instance tool is not "copilot".
func TestBuildCopilotCommand_NonCopilotTool(t *testing.T) {
	defer copilotCmdTestEnv(t)()

	inst := NewInstance("shell-test", "/tmp/test")
	cmd := inst.buildCopilotCommand("bash")
	if cmd != "bash" {
		t.Errorf("Non-copilot tool should pass baseCommand through unchanged, got: %q", cmd)
	}
}

// TestBuildCopilotCommand_YoloMode verifies --yolo is appended when YoloMode
// is enabled via per-session CopilotOptions.
func TestBuildCopilotCommand_YoloMode(t *testing.T) {
	defer copilotCmdTestEnv(t)()

	inst := NewInstanceWithTool("test", "/tmp/test", "copilot")
	yes := true
	if err := inst.SetCopilotOptions(&CopilotOptions{YoloMode: &yes}); err != nil {
		t.Fatalf("SetCopilotOptions: %v", err)
	}

	cmd := inst.buildCopilotCommand("copilot")
	if !strings.Contains(cmd, "--yolo") {
		t.Errorf("YoloMode=true should add --yolo, got: %s", cmd)
	}
}

// TestBuildCopilotCommand_YoloModeOverridesGlobal verifies an explicit
// per-session YoloMode=false beats a global config yolo_mode=true.
func TestBuildCopilotCommand_YoloModeOverridesGlobal(t *testing.T) {
	defer copilotCmdTestEnv(t)()

	tmp := os.Getenv("HOME")
	configDir := filepath.Join(tmp, ".agent-deck")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"),
		[]byte("[copilot]\nyolo_mode = true\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()

	inst := NewInstanceWithTool("test", "/tmp/test", "copilot")
	no := false
	if err := inst.SetCopilotOptions(&CopilotOptions{YoloMode: &no}); err != nil {
		t.Fatalf("SetCopilotOptions: %v", err)
	}

	cmd := inst.buildCopilotCommand("copilot")
	if strings.Contains(cmd, "--yolo") {
		t.Errorf("Per-session YoloMode=false should override global config=true, got: %s", cmd)
	}
}

// TestBuildCopilotCommand_Model verifies --model <name> is appended when
// CopilotOptions.Model is set.
func TestBuildCopilotCommand_Model(t *testing.T) {
	defer copilotCmdTestEnv(t)()

	inst := NewInstanceWithTool("test", "/tmp/test", "copilot")
	if err := inst.SetCopilotOptions(&CopilotOptions{Model: "gpt-5.2"}); err != nil {
		t.Fatalf("SetCopilotOptions: %v", err)
	}

	cmd := inst.buildCopilotCommand("copilot")
	if !strings.Contains(cmd, "--model gpt-5.2") {
		t.Errorf("Should contain --model gpt-5.2, got: %s", cmd)
	}
}

// TestBuildCopilotCommand_Autopilot verifies --autopilot is appended when
// AutopilotMode is enabled per-session.
func TestBuildCopilotCommand_Autopilot(t *testing.T) {
	defer copilotCmdTestEnv(t)()

	inst := NewInstanceWithTool("test", "/tmp/test", "copilot")
	yes := true
	if err := inst.SetCopilotOptions(&CopilotOptions{AutopilotMode: &yes}); err != nil {
		t.Fatalf("SetCopilotOptions: %v", err)
	}

	cmd := inst.buildCopilotCommand("copilot")
	if !strings.Contains(cmd, "--autopilot") {
		t.Errorf("AutopilotMode=true should add --autopilot, got: %s", cmd)
	}
}

// TestBuildCopilotCommand_SubagentAddDir verifies sub-sessions get --add-dir
// pointing at the parent's project directory.
func TestBuildCopilotCommand_SubagentAddDir(t *testing.T) {
	defer copilotCmdTestEnv(t)()

	inst := NewInstanceWithTool("subagent", "/tmp/sub-workdir", "copilot")
	inst.SetParentWithPath("parent-id-123", "/home/user/projects/main-project")

	cmd := inst.buildCopilotCommand("copilot")
	if !strings.Contains(cmd, "--add-dir /home/user/projects/main-project") {
		t.Errorf("Subagent should contain --add-dir with parent path, got: %s", cmd)
	}

	standalone := NewInstanceWithTool("standalone", "/tmp/standalone", "copilot")
	cmdNo := standalone.buildCopilotCommand("copilot")
	if strings.Contains(cmdNo, "--add-dir") {
		t.Errorf("Standalone agent should NOT have --add-dir, got: %s", cmdNo)
	}
}

// TestBuildCopilotCommand_ConfigDirPrefix verifies COPILOT_HOME is
// prepended when [copilot].config_dir is set in user config.
func TestBuildCopilotCommand_ConfigDirPrefix(t *testing.T) {
	defer copilotCmdTestEnv(t)()

	tmp := os.Getenv("HOME")
	configDir := filepath.Join(tmp, ".agent-deck")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"),
		[]byte("[copilot]\nconfig_dir = \"/tmp/custom-copilot\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()

	inst := NewInstanceWithTool("test", "/tmp/test", "copilot")
	cmd := inst.buildCopilotCommand("copilot")

	if !strings.Contains(cmd, "COPILOT_HOME=/tmp/custom-copilot") {
		t.Errorf("Should contain COPILOT_HOME prefix, got: %s", cmd)
	}
}

// TestBuildCopilotCommand_CustomCommand verifies [copilot].command in config
// overrides the default binary name.
func TestBuildCopilotCommand_CustomCommand(t *testing.T) {
	defer copilotCmdTestEnv(t)()

	tmp := os.Getenv("HOME")
	configDir := filepath.Join(tmp, ".agent-deck")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"),
		[]byte("[copilot]\ncommand = \"my-copilot\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()

	inst := NewInstanceWithTool("test", "/tmp/test", "copilot")
	cmd := inst.buildCopilotCommand("copilot")

	if !strings.Contains(cmd, "my-copilot") {
		t.Errorf("Should use custom command from config, got: %s", cmd)
	}
}
