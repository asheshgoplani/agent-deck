package session

import (
	"reflect"
	"testing"
)

// xAI Grok Build CLI support. These tests define the Tool="grok" contract:
// options marshalling, ToArgs for model + --always-approve, factory from
// config, and the command builder (bare name, config defaults, per-session
// options, command override, passthrough, wrong-tool gate).
//
// Binary: `grok` (https://docs.x.ai/build/overview). Claude-Code-style TUI.
// Key flags: -m/--model, --always-approve.

func TestGrokOptions_ToolName(t *testing.T) {
	opts := &GrokOptions{}
	if got := opts.ToolName(); got != "grok" {
		t.Errorf("GrokOptions.ToolName() = %q, want %q", got, "grok")
	}
}

func TestGrokOptions_ToArgs(t *testing.T) {
	yoloTrue := true
	yoloFalse := false

	tests := []struct {
		name     string
		opts     GrokOptions
		expected []string
	}{
		{"default - no args", GrokOptions{}, nil},
		{"yolo nil - no args", GrokOptions{YoloMode: nil}, nil},
		{"yolo false - no args", GrokOptions{YoloMode: &yoloFalse}, nil},
		{"yolo true", GrokOptions{YoloMode: &yoloTrue}, []string{"--always-approve"}},
		{"model only", GrokOptions{Model: "grok-build"}, []string{"-m", "grok-build"}},
		{"model + yolo", GrokOptions{Model: "grok-build", YoloMode: &yoloTrue}, []string{"-m", "grok-build", "--always-approve"}},
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

func TestNewGrokOptions_Defaults(t *testing.T) {
	opts := NewGrokOptions(nil)
	if opts == nil {
		t.Fatal("NewGrokOptions(nil) returned nil")
	}
	if opts.YoloMode != nil {
		t.Errorf("default YoloMode = %v, want nil", opts.YoloMode)
	}
	if opts.Model != "" {
		t.Errorf("default Model = %q, want empty", opts.Model)
	}
}

func TestNewGrokOptions_FromConfig(t *testing.T) {
	cfg := &UserConfig{
		Grok: GrokSettings{YoloMode: true, DefaultModel: "grok-build"},
	}
	opts := NewGrokOptions(cfg)
	if opts == nil {
		t.Fatal("NewGrokOptions returned nil")
	}
	if opts.YoloMode == nil || !*opts.YoloMode {
		t.Errorf("YoloMode = %v, want true", opts.YoloMode)
	}
	if opts.Model != "grok-build" {
		t.Errorf("Model = %q, want %q", opts.Model, "grok-build")
	}
}

func TestGrokOptions_MarshalUnmarshalRoundtrip(t *testing.T) {
	yolo := true
	orig := &GrokOptions{Model: "grok-build", YoloMode: &yolo}

	data, err := MarshalToolOptions(orig)
	if err != nil {
		t.Fatalf("MarshalToolOptions error: %v", err)
	}

	got, err := UnmarshalGrokOptions(data)
	if err != nil {
		t.Fatalf("UnmarshalGrokOptions error: %v", err)
	}
	if got == nil {
		t.Fatal("UnmarshalGrokOptions returned nil")
	}
	if !reflect.DeepEqual(got, orig) {
		t.Errorf("roundtrip = %+v, want %+v", got, orig)
	}
}

func TestUnmarshalGrokOptions_WrongTool(t *testing.T) {
	// A codex wrapper must not be decoded as grok options.
	data, err := MarshalToolOptions(&CodexOptions{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("MarshalToolOptions error: %v", err)
	}
	got, err := UnmarshalGrokOptions(data)
	if err != nil {
		t.Fatalf("UnmarshalGrokOptions error: %v", err)
	}
	if got != nil {
		t.Errorf("UnmarshalGrokOptions on codex wrapper = %+v, want nil", got)
	}
}

func TestBuildGrokCommand_BareName(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()
	userConfigCache = &UserConfig{}

	inst := &Instance{Tool: "grok"}
	cmd := inst.buildGrokCommand("grok")
	if !endsWith(cmd, "grok") {
		t.Errorf("buildGrokCommand(\"grok\") = %q, want suffix %q", cmd, "grok")
	}
}

func TestBuildGrokCommand_YoloFromConfig(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()
	userConfigCache = &UserConfig{Grok: GrokSettings{YoloMode: true}}

	inst := &Instance{Tool: "grok"}
	cmd := inst.buildGrokCommand("grok")
	if !endsWith(cmd, "grok --always-approve") {
		t.Errorf("buildGrokCommand() = %q, want suffix %q", cmd, "grok --always-approve")
	}
}

func TestBuildGrokCommand_DefaultModelFromConfig(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()
	userConfigCache = &UserConfig{Grok: GrokSettings{DefaultModel: "grok-build"}}

	inst := &Instance{Tool: "grok"}
	cmd := inst.buildGrokCommand("grok")
	if !endsWith(cmd, "grok -m grok-build") {
		t.Errorf("buildGrokCommand() = %q, want suffix %q", cmd, "grok -m grok-build")
	}
}

func TestBuildGrokCommand_PerSessionOptions(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()
	// Config has no defaults; per-session options should drive the flags.
	userConfigCache = &UserConfig{}

	inst := &Instance{Tool: "grok"}
	if err := inst.ApplyLaunchModel("grok-build"); err != nil {
		t.Fatalf("ApplyLaunchModel error: %v", err)
	}
	cmd := inst.buildGrokCommand("grok")
	if !endsWith(cmd, "grok -m grok-build") {
		t.Errorf("buildGrokCommand() = %q, want suffix %q", cmd, "grok -m grok-build")
	}
}

// A yolo-only per-session override (e.g. from `--yolo`) must still inherit the
// configured default_model: a partial wrapper would otherwise skip the config
// fallback and silently drop the model.
func TestBuildGrokCommand_PartialOptionsInheritConfigModel(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()
	userConfigCache = &UserConfig{Grok: GrokSettings{DefaultModel: "grok-build"}}

	inst := &Instance{Tool: "grok"}
	yolo := true
	if err := inst.SetGrokOptions(&GrokOptions{YoloMode: &yolo}); err != nil {
		t.Fatalf("SetGrokOptions error: %v", err)
	}
	cmd := inst.buildGrokCommand("grok")
	if !endsWith(cmd, "grok -m grok-build --always-approve") {
		t.Errorf("buildGrokCommand() = %q, want suffix %q", cmd, "grok -m grok-build --always-approve")
	}
}

func TestBuildGrokCommand_CommandOverride(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()
	userConfigCache = &UserConfig{Grok: GrokSettings{Command: "grok --reasoning-effort high"}}

	inst := &Instance{Tool: "grok"}
	cmd := inst.buildGrokCommand("grok")
	if !endsWith(cmd, "grok --reasoning-effort high") {
		t.Errorf("buildGrokCommand() = %q, want suffix %q", cmd, "grok --reasoning-effort high")
	}
}

func TestBuildGrokCommand_Passthrough(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()
	userConfigCache = &UserConfig{}

	inst := &Instance{Tool: "grok"}
	got := inst.buildGrokCommand("grok --some-flag")
	if !endsWith(got, "grok --some-flag") {
		t.Errorf("buildGrokCommand passthrough = %q, want suffix %q", got, "grok --some-flag")
	}
}

func TestBuildGrokCommand_WrongTool(t *testing.T) {
	inst := &Instance{Tool: "claude"}
	got := inst.buildGrokCommand("anything")
	if got != "anything" {
		t.Errorf("buildGrokCommand with wrong tool = %q, want %q", got, "anything")
	}
}

func TestGetToolEnvFile_Grok(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()
	userConfigCache = &UserConfig{Grok: GrokSettings{EnvFile: "/tmp/grok.env"}}

	inst := &Instance{Tool: "grok"}
	if got := inst.getToolEnvFile(); got != "/tmp/grok.env" {
		t.Errorf("getToolEnvFile() for grok = %q, want %q", got, "/tmp/grok.env")
	}
}
