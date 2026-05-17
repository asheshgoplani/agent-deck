package session

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Issue #940: charmbracelet/crush CLI support.
// These tests define the Tool="crush" contract: options marshalling,
// ToArgs for yolo + resume, factory from config, and the basic identity
// gates (icon, IsClaudeCompatible, builtin-name filter).
//
// Binary: `crush` (github.com/charmbracelet/crush). Interactive TUI.
// Key flags: --yolo (-y), --session/-s <id>, --continue/-C, --cwd, --debug.

func TestCrushOptions_ToolName(t *testing.T) {
	opts := &CrushOptions{}
	if got := opts.ToolName(); got != "crush" {
		t.Errorf("CrushOptions.ToolName() = %q, want %q", got, "crush")
	}
}

func TestCrushOptions_ToArgs(t *testing.T) {
	yoloTrue := true
	yoloFalse := false

	tests := []struct {
		name     string
		opts     CrushOptions
		expected []string
	}{
		{
			name:     "default - no args",
			opts:     CrushOptions{},
			expected: nil,
		},
		{
			name:     "yolo nil - no args",
			opts:     CrushOptions{YoloMode: nil},
			expected: nil,
		},
		{
			name:     "yolo false - no args",
			opts:     CrushOptions{YoloMode: &yoloFalse},
			expected: nil,
		},
		{
			name:     "yolo true - --yolo present",
			opts:     CrushOptions{YoloMode: &yoloTrue},
			expected: []string{"--yolo"},
		},
		{
			name:     "resume with id",
			opts:     CrushOptions{ResumeSessionID: "abc123"},
			expected: []string{"--session", "abc123"},
		},
		{
			name:     "continue last",
			opts:     CrushOptions{ContinueLast: true},
			expected: []string{"--continue"},
		},
		{
			name:     "yolo + resume",
			opts:     CrushOptions{YoloMode: &yoloTrue, ResumeSessionID: "sess-42"},
			expected: []string{"--session", "sess-42", "--yolo"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.opts.ToArgs()
			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("ToArgs() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestNewCrushOptions_Defaults(t *testing.T) {
	opts := NewCrushOptions(nil)
	if opts == nil {
		t.Fatal("NewCrushOptions(nil) returned nil")
	}
	if opts.YoloMode != nil {
		t.Errorf("default YoloMode = %v, want nil", opts.YoloMode)
	}
}

func TestNewCrushOptions_WithYoloConfig(t *testing.T) {
	cfg := &UserConfig{
		Crush: CrushSettings{YoloMode: true},
	}
	opts := NewCrushOptions(cfg)
	if opts == nil {
		t.Fatal("NewCrushOptions returned nil")
	}
	if opts.YoloMode == nil || !*opts.YoloMode {
		t.Errorf("YoloMode = %v, want true", opts.YoloMode)
	}
}

func TestNewCrushOptions_WithoutYoloConfig(t *testing.T) {
	cfg := &UserConfig{
		Crush: CrushSettings{YoloMode: false},
	}
	opts := NewCrushOptions(cfg)
	if opts == nil {
		t.Fatal("NewCrushOptions returned nil")
	}
	if opts.YoloMode != nil {
		t.Errorf("YoloMode = %v, want nil (not set when config is false)", opts.YoloMode)
	}
}

func TestCrushOptions_MarshalUnmarshalRoundtrip(t *testing.T) {
	yolo := true
	orig := &CrushOptions{YoloMode: &yolo, ResumeSessionID: "sess-42"}

	data, err := MarshalToolOptions(orig)
	if err != nil {
		t.Fatalf("MarshalToolOptions: %v", err)
	}

	var wrapper ToolOptionsWrapper
	if err := json.Unmarshal(data, &wrapper); err != nil {
		t.Fatalf("unmarshal wrapper: %v", err)
	}
	if wrapper.Tool != "crush" {
		t.Errorf("wrapper.Tool = %q, want %q", wrapper.Tool, "crush")
	}

	got, err := UnmarshalCrushOptions(data)
	if err != nil {
		t.Fatalf("UnmarshalCrushOptions: %v", err)
	}
	if got == nil {
		t.Fatal("UnmarshalCrushOptions returned nil")
	}
	if got.YoloMode == nil || !*got.YoloMode {
		t.Errorf("roundtrip YoloMode = %v, want true", got.YoloMode)
	}
	if got.ResumeSessionID != "sess-42" {
		t.Errorf("roundtrip ResumeSessionID = %q, want %q", got.ResumeSessionID, "sess-42")
	}
}

func TestUnmarshalCrushOptions_WrongTool(t *testing.T) {
	raw := json.RawMessage(`{"tool":"codex","options":{}}`)
	got, err := UnmarshalCrushOptions(raw)
	if err != nil {
		t.Fatalf("UnmarshalCrushOptions: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for wrong tool, got %+v", got)
	}
}

func TestIsClaudeCompatible_CrushNotCompatible(t *testing.T) {
	if IsClaudeCompatible("crush") {
		t.Error("IsClaudeCompatible(\"crush\") must be false")
	}
}

func TestGetToolIcon_Crush(t *testing.T) {
	icon := GetToolIcon("crush")
	if icon == "" {
		t.Error("GetToolIcon(\"crush\") returned empty")
	}
	if icon == GetToolIcon("shell") {
		t.Errorf("GetToolIcon(\"crush\") = %q equals shell fallback (want a distinct icon)", icon)
	}
}

func TestGetCustomToolNames_CrushIsBuiltin(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()

	userConfigCache = &UserConfig{
		Tools: map[string]ToolDef{
			"crush":      {Command: "crush"},
			"my-wrapper": {Command: "claude"},
		},
	}

	names := GetCustomToolNames()
	for _, n := range names {
		if n == "crush" {
			t.Errorf("GetCustomToolNames() returned %q as custom; crush is built-in", n)
		}
	}
}

func TestNewInstanceWithTool_Crush(t *testing.T) {
	inst := NewInstanceWithTool("crush-test", "/tmp/crush-test-proj", "crush")
	if inst == nil {
		t.Fatal("NewInstanceWithTool returned nil")
	}
	if inst.Tool != "crush" {
		t.Errorf("inst.Tool = %q, want %q", inst.Tool, "crush")
	}
}

func TestBuildCrushCommand_BareName(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()
	userConfigCache = &UserConfig{}

	inst := &Instance{Tool: "crush"}
	cmd := inst.buildCrushCommand("crush")
	if cmd == "" {
		t.Fatal("buildCrushCommand(\"crush\") returned empty")
	}
	// Should end with the bare "crush" binary when no config is present
	if !endsWith(cmd, "crush") {
		t.Errorf("buildCrushCommand(\"crush\") = %q, want suffix \"crush\"", cmd)
	}
}

func TestBuildCrushCommand_YoloFromConfig(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()
	userConfigCache = &UserConfig{
		Crush: CrushSettings{YoloMode: true},
	}

	inst := &Instance{Tool: "crush"}
	cmd := inst.buildCrushCommand("crush")
	if !endsWith(cmd, "crush --yolo") {
		t.Errorf("buildCrushCommand() = %q, want suffix %q", cmd, "crush --yolo")
	}
}

func TestBuildCrushCommand_CommandOverride(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()
	userConfigCache = &UserConfig{
		Crush: CrushSettings{Command: "crush --debug"},
	}

	inst := &Instance{Tool: "crush"}
	cmd := inst.buildCrushCommand("crush")
	if !endsWith(cmd, "crush --debug") {
		t.Errorf("buildCrushCommand() = %q, want suffix %q", cmd, "crush --debug")
	}
}

func TestBuildCrushCommand_Passthrough(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()
	userConfigCache = &UserConfig{}

	inst := &Instance{Tool: "crush"}
	got := inst.buildCrushCommand("crush --some-flag")
	if !endsWith(got, "crush --some-flag") {
		t.Errorf("buildCrushCommand passthrough = %q, want suffix %q", got, "crush --some-flag")
	}
}

func TestBuildCrushCommand_WrongTool(t *testing.T) {
	inst := &Instance{Tool: "claude"}
	got := inst.buildCrushCommand("anything")
	if got != "anything" {
		t.Errorf("buildCrushCommand with wrong tool = %q, want %q", got, "anything")
	}
}

func TestGetToolEnvFile_Crush(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()
	userConfigCache = &UserConfig{
		Crush: CrushSettings{EnvFile: "/tmp/crush.env"},
	}

	inst := &Instance{Tool: "crush"}
	got := inst.getToolEnvFile()
	if got != "/tmp/crush.env" {
		t.Errorf("getToolEnvFile() for crush = %q, want %q", got, "/tmp/crush.env")
	}
}

// endsWith is a small local helper to avoid pulling strings into the test
// file boilerplate twice (and to mirror the suffix-style assertions used by
// neighbouring adapter tests).
func endsWith(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
