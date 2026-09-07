package session

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Muse Code CLI support (Meta's `muse` binary, "Muse Code" TUI).
// These tests define the Tool="muse" contract: options marshalling,
// ToArgs for yolo, factory from config, launch-command building with the
// empirically required --trust-workspace default, and the basic identity
// gates (icon, IsClaudeCompatible, builtin-name filter).
//
// Binary: `muse`. Interactive TUI.
// Key flags: --trust-workspace, --yolo, --provider, --model.
// Resume: `muse resume <uuid>` subcommand (wired in a follow-up).
//
// Launch evidence (captured live from Muse Code 1.0.2 in a tmux pane):
//   - bare `muse` blocks on "Do you trust this workspace?" in a fresh dir,
//     so the default command carries --trust-workspace.
//   - idle prompt: `⟩` + "Type @ to search and insert workspace file paths".
//   - busy: `◈ Thinking (Ns · esc to interrupt)`.
//   - assistant message marker: `◆`.
//   - status bar: `<provider> · /abs/path`.

func TestMuseOptions_ToolName(t *testing.T) {
	opts := &MuseOptions{}
	if got := opts.ToolName(); got != "muse" {
		t.Errorf("MuseOptions.ToolName() = %q, want %q", got, "muse")
	}
}

func TestMuseOptions_ToArgs(t *testing.T) {
	yoloTrue := true
	yoloFalse := false

	tests := []struct {
		name     string
		opts     MuseOptions
		expected []string
	}{
		{
			name:     "default - no args",
			opts:     MuseOptions{},
			expected: nil,
		},
		{
			name:     "yolo nil - no args",
			opts:     MuseOptions{YoloMode: nil},
			expected: nil,
		},
		{
			name:     "yolo false - no args",
			opts:     MuseOptions{YoloMode: &yoloFalse},
			expected: nil,
		},
		{
			name:     "yolo true - --yolo present",
			opts:     MuseOptions{YoloMode: &yoloTrue},
			expected: []string{"--yolo"},
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

func TestNewMuseOptions_Defaults(t *testing.T) {
	opts := NewMuseOptions(nil)
	if opts == nil {
		t.Fatal("NewMuseOptions(nil) returned nil")
	}
	if opts.YoloMode != nil {
		t.Errorf("default YoloMode = %v, want nil", opts.YoloMode)
	}
}

func TestNewMuseOptions_WithYoloConfig(t *testing.T) {
	cfg := &UserConfig{
		Muse: MuseSettings{YoloMode: true},
	}
	opts := NewMuseOptions(cfg)
	if opts == nil {
		t.Fatal("NewMuseOptions returned nil")
	}
	if opts.YoloMode == nil || !*opts.YoloMode {
		t.Errorf("YoloMode = %v, want true", opts.YoloMode)
	}
}

func TestNewMuseOptions_WithoutYoloConfig(t *testing.T) {
	cfg := &UserConfig{
		Muse: MuseSettings{YoloMode: false},
	}
	opts := NewMuseOptions(cfg)
	if opts == nil {
		t.Fatal("NewMuseOptions returned nil")
	}
	if opts.YoloMode != nil {
		t.Errorf("YoloMode = %v, want nil (not set when config is false)", opts.YoloMode)
	}
}

func TestMuseOptions_MarshalUnmarshalRoundtrip(t *testing.T) {
	yolo := true
	orig := &MuseOptions{YoloMode: &yolo}

	data, err := MarshalToolOptions(orig)
	if err != nil {
		t.Fatalf("MarshalToolOptions: %v", err)
	}

	var wrapper ToolOptionsWrapper
	if err := json.Unmarshal(data, &wrapper); err != nil {
		t.Fatalf("unmarshal wrapper: %v", err)
	}
	if wrapper.Tool != "muse" {
		t.Errorf("wrapper.Tool = %q, want %q", wrapper.Tool, "muse")
	}

	got, err := UnmarshalMuseOptions(data)
	if err != nil {
		t.Fatalf("UnmarshalMuseOptions: %v", err)
	}
	if got == nil {
		t.Fatal("UnmarshalMuseOptions returned nil")
	}
	if got.YoloMode == nil || !*got.YoloMode {
		t.Errorf("roundtrip YoloMode = %v, want true", got.YoloMode)
	}
}

func TestUnmarshalMuseOptions_WrongTool(t *testing.T) {
	raw := json.RawMessage(`{"tool":"codex","options":{}}`)
	got, err := UnmarshalMuseOptions(raw)
	if err != nil {
		t.Fatalf("UnmarshalMuseOptions: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for wrong tool, got %+v", got)
	}
}

func TestIsClaudeCompatible_MuseNotCompatible(t *testing.T) {
	if IsClaudeCompatible("muse") {
		t.Error("IsClaudeCompatible(\"muse\") must be false")
	}
}

func TestGetToolIcon_Muse(t *testing.T) {
	icon := GetToolIcon("muse")
	if icon == "" {
		t.Error("GetToolIcon(\"muse\") returned empty")
	}
	if icon == GetToolIcon("shell") {
		t.Errorf("GetToolIcon(\"muse\") = %q equals shell fallback (want a distinct icon)", icon)
	}
}

func TestGetCustomToolNames_MuseIsBuiltin(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()

	userConfigCache = &UserConfig{
		Tools: map[string]ToolDef{
			"muse":       {Command: "muse"},
			"my-wrapper": {Command: "claude"},
		},
	}

	names := GetCustomToolNames()
	for _, n := range names {
		if n == "muse" {
			t.Errorf("GetCustomToolNames() returned %q as custom; muse is built-in", n)
		}
	}
}

func TestNewInstanceWithTool_Muse(t *testing.T) {
	inst := NewInstanceWithTool("muse-test", "/tmp/muse-test-proj", "muse")
	if inst == nil {
		t.Fatal("NewInstanceWithTool returned nil")
	}
	if inst.Tool != "muse" {
		t.Errorf("inst.Tool = %q, want %q", inst.Tool, "muse")
	}
}

func TestGetMuseCommand_Default(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()
	userConfigCache = &UserConfig{}

	// Bare muse blocks on the workspace-trust prompt in a fresh directory
	// (captured live), so the default carries --trust-workspace.
	if got := GetMuseCommand(); got != "muse --trust-workspace" {
		t.Errorf("GetMuseCommand() = %q, want %q", got, "muse --trust-workspace")
	}
}

func TestGetMuseCommand_Override(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()
	userConfigCache = &UserConfig{
		Muse: MuseSettings{Command: "muse --provider echo"},
	}

	if got := GetMuseCommand(); got != "muse --provider echo" {
		t.Errorf("GetMuseCommand() = %q, want override", got)
	}
}

func TestBuildMuseCommand_BareName(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()
	userConfigCache = &UserConfig{}

	inst := &Instance{Tool: "muse"}
	cmd := inst.buildMuseCommand("muse")
	if cmd == "" {
		t.Fatal("buildMuseCommand(\"muse\") returned empty")
	}
	if !strings.HasSuffix(cmd, "muse --trust-workspace") {
		t.Errorf("buildMuseCommand(\"muse\") = %q, want suffix %q", cmd, "muse --trust-workspace")
	}
}

func TestBuildMuseCommand_YoloFromConfig(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()
	userConfigCache = &UserConfig{
		Muse: MuseSettings{YoloMode: true},
	}

	inst := &Instance{Tool: "muse"}
	cmd := inst.buildMuseCommand("muse")
	if !strings.HasSuffix(cmd, "muse --trust-workspace --yolo") {
		t.Errorf("buildMuseCommand() = %q, want suffix %q", cmd, "muse --trust-workspace --yolo")
	}
}

func TestBuildMuseCommand_YoloFromOptions(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()
	userConfigCache = &UserConfig{}

	yolo := true
	opts, err := MarshalToolOptions(&MuseOptions{YoloMode: &yolo})
	if err != nil {
		t.Fatalf("MarshalToolOptions: %v", err)
	}

	inst := &Instance{Tool: "muse", ToolOptionsJSON: opts}
	cmd := inst.buildMuseCommand("muse")
	if !strings.HasSuffix(cmd, "muse --trust-workspace --yolo") {
		t.Errorf("buildMuseCommand() = %q, want suffix %q", cmd, "muse --trust-workspace --yolo")
	}
}

func TestBuildMuseCommand_CommandOverride(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()
	userConfigCache = &UserConfig{
		Muse: MuseSettings{Command: "muse --provider echo"},
	}

	inst := &Instance{Tool: "muse"}
	cmd := inst.buildMuseCommand("muse")
	if !strings.HasSuffix(cmd, "muse --provider echo") {
		t.Errorf("buildMuseCommand() = %q, want suffix %q", cmd, "muse --provider echo")
	}
}

func TestBuildMuseCommand_Passthrough(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()
	userConfigCache = &UserConfig{}

	inst := &Instance{Tool: "muse"}
	got := inst.buildMuseCommand("muse --provider echo")
	if !strings.HasSuffix(got, "muse --provider echo") {
		t.Errorf("buildMuseCommand passthrough = %q, want suffix %q", got, "muse --provider echo")
	}
}

func TestBuildMuseCommand_WrongTool(t *testing.T) {
	inst := &Instance{Tool: "claude"}
	got := inst.buildMuseCommand("anything")
	if got != "anything" {
		t.Errorf("buildMuseCommand with wrong tool = %q, want %q", got, "anything")
	}
}

func TestBuildMuseResumeCommand(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()
	userConfigCache = &UserConfig{}

	inst := &Instance{Tool: "muse", Command: "muse"}
	cmd := inst.buildMuseResumeCommand("sess-uuid-1")
	if !strings.HasSuffix(cmd, "muse --trust-workspace resume sess-uuid-1") {
		t.Errorf("buildMuseResumeCommand() = %q, want suffix %q", cmd, "muse --trust-workspace resume sess-uuid-1")
	}
}

func TestBuildMuseResumeCommand_Yolo(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()
	userConfigCache = &UserConfig{
		Muse: MuseSettings{YoloMode: true},
	}

	inst := &Instance{Tool: "muse", Command: "muse"}
	cmd := inst.buildMuseResumeCommand("sess-uuid-1")
	if !strings.HasSuffix(cmd, "muse --trust-workspace resume sess-uuid-1 --yolo") {
		t.Errorf("buildMuseResumeCommand() = %q, want yolo suffix", cmd)
	}
}

func TestBuildMuseResumeCommand_ExplicitYoloFalseBeatsConfig(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()
	userConfigCache = &UserConfig{
		Muse: MuseSettings{YoloMode: true},
	}

	yolo := false
	opts, err := MarshalToolOptions(&MuseOptions{YoloMode: &yolo})
	if err != nil {
		t.Fatalf("MarshalToolOptions: %v", err)
	}
	inst := &Instance{Tool: "muse", Command: "muse", ToolOptionsJSON: opts}
	cmd := inst.buildMuseResumeCommand("sess-uuid-1")
	if strings.Contains(cmd, "--yolo") {
		t.Errorf("explicit yolo=false must suppress config yolo, got %q", cmd)
	}
	if !strings.HasSuffix(cmd, "resume sess-uuid-1") {
		t.Errorf("buildMuseResumeCommand() = %q, want resume suffix", cmd)
	}
}

func TestBuildMuseCommand_NilYoloInheritsConfig(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()
	userConfigCache = &UserConfig{
		Muse: MuseSettings{YoloMode: true},
	}

	// A serialized default (YoloMode nil) is not a choice: the global
	// config applies instead of being silently dropped.
	opts, err := MarshalToolOptions(&MuseOptions{})
	if err != nil {
		t.Fatalf("MarshalToolOptions: %v", err)
	}
	inst := &Instance{Tool: "muse", Command: "muse", ToolOptionsJSON: opts}
	if cmd := inst.buildMuseCommand("muse"); !strings.HasSuffix(cmd, "muse --trust-workspace --yolo") {
		t.Errorf("nil YoloMode must inherit config yolo, got %q", cmd)
	}
	if cmd := inst.buildMuseResumeCommand("sess-uuid-1"); !strings.HasSuffix(cmd, "resume sess-uuid-1 --yolo") {
		t.Errorf("nil YoloMode must inherit config yolo on resume, got %q", cmd)
	}
}

func TestBuildMuseResumeCommand_EmptyIDFallsBackToFresh(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()
	userConfigCache = &UserConfig{}

	inst := &Instance{Tool: "muse", Command: "muse"}
	cmd := inst.buildMuseResumeCommand("  ")
	if strings.Contains(cmd, "resume") {
		t.Errorf("empty session id must not emit resume, got %q", cmd)
	}
	if !strings.HasSuffix(cmd, "muse --trust-workspace") {
		t.Errorf("empty session id must boot fresh, got %q", cmd)
	}
}

func TestBuildMuseResumeCommand_WrongTool(t *testing.T) {
	inst := &Instance{Tool: "claude"}
	if got := inst.buildMuseResumeCommand("sess-uuid-1"); got != "" {
		t.Errorf("buildMuseResumeCommand with wrong tool = %q, want empty", got)
	}
}

func TestBuildMuseResumeCommand_CustomCommandPreserved(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()
	userConfigCache = &UserConfig{
		Muse: MuseSettings{YoloMode: true},
	}

	// A session launched with a custom invocation (wrapper/provider flags)
	// must resume through it, verbatim: no default substitution and no
	// flag injection, mirroring the fresh passthrough path.
	inst := &Instance{Tool: "muse", Command: "muse --provider echo"}
	cmd := inst.buildMuseResumeCommand("sess-uuid-1")
	if !strings.HasSuffix(cmd, "muse --provider echo resume sess-uuid-1") {
		t.Errorf("buildMuseResumeCommand() = %q, want custom command preserved with resume suffix", cmd)
	}
	if strings.Contains(cmd, "--yolo") {
		t.Errorf("passthrough resume must not inject --yolo, got %q", cmd)
	}
	if strings.Contains(cmd, "--trust-workspace") {
		t.Errorf("custom base must replace the default wholesale, got %q", cmd)
	}
}

func TestGetToolEnvFile_Muse(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()
	userConfigCache = &UserConfig{
		Muse: MuseSettings{EnvFile: "/tmp/muse.env"},
	}

	inst := &Instance{Tool: "muse"}
	got := inst.getToolEnvFile()
	if got != "/tmp/muse.env" {
		t.Errorf("getToolEnvFile() for muse = %q, want %q", got, "/tmp/muse.env")
	}
}

func TestMatchTool_Muse(t *testing.T) {
	r := Init(nil)
	for _, cmd := range []string{
		"muse",
		"muse --trust-workspace",
		"muse --yolo",
		"MUSE",
		"muse resume 01a063dd-45dd-7402-b516-4346e923db3e",
		"/usr/local/bin/muse",
		"/usr/local/bin/muse --trust-workspace",
	} {
		if got := r.Match(cmd); got != "muse" {
			t.Errorf("Match(%q) = %q, want %q", cmd, got, "muse")
		}
	}
}

func TestMatchTool_Muse_Negative(t *testing.T) {
	// Executable-position match only: the name must lead the command.
	r := Init(nil)
	for _, cmd := range []string{
		"echo muse",
		"amuse",
		"echo museum",
		"git -C /tmp/muse-project status",
		// Non-leading occurrences fail closed, even for wrappers: only
		// the executable slot classifies.
		"my-wrapper muse --flag",
		"my-wrapper muse",
	} {
		if got := r.Match(cmd); got == "muse" {
			t.Errorf("Match(%q) = %q, should NOT match muse", cmd, got)
		}
	}
}
