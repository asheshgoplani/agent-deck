package session

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Goose Agent CLI support.
// These tests define the Tool="goose" contract: BuildGooseCommand,
// prompt/busy detection, options marshalling, and identity gates.

func TestBuildGooseCommand_BareName(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()
	userConfigCache = &UserConfig{}

	settings := DefaultGooseSettings()
	args := BuildGooseCommand(settings, "/tmp/project", "")
	if len(args) == 0 {
		t.Fatal("BuildGooseCommand returned empty")
	}
	if args[0] != "goose" {
		t.Errorf("BuildGooseCommand args[0] = %q, want %q", args[0], "goose")
	}
}

func TestBuildGooseCommand_YoloMode(t *testing.T) {
	settings := DefaultGooseSettings()
	settings.YoloMode = true
	args := BuildGooseCommand(settings, "/tmp/project", "")

	found := false
	for _, a := range args {
		if a == "--auto" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("BuildGooseCommand with YoloMode=true should include --auto, got %v", args)
	}
}

func TestBuildGooseCommand_Profile(t *testing.T) {
	settings := DefaultGooseSettings()
	args := BuildGooseCommand(settings, "/tmp/project", "work")

	found := false
	for i, a := range args {
		if a == "--profile" && i+1 < len(args) && args[i+1] == "work" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("BuildGooseCommand with profile=work should include --profile work, got %v", args)
	}
}

func TestBuildGooseCommand_CustomCommand(t *testing.T) {
	settings := &GooseSettings{Command: "/opt/goose/bin/goose --verbose"}
	args := BuildGooseCommand(settings, "/tmp/project", "")
	if args[0] != "/opt/goose/bin/goose" {
		t.Errorf("BuildGooseCommand custom command binary = %q, want %q", args[0], "/opt/goose/bin/goose")
	}
	found := false
	for _, a := range args {
		if a == "--verbose" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("BuildGooseCommand custom command should preserve --verbose, got %v", args)
	}
}

func TestBuildGooseCommand_NilSettings(t *testing.T) {
	args := BuildGooseCommand(nil, "/tmp/project", "")
	if len(args) == 0 {
		t.Fatal("BuildGooseCommand(nil) returned empty")
	}
	if args[0] != "goose" {
		t.Errorf("BuildGooseCommand(nil) args[0] = %q, want %q", args[0], "goose")
	}
}

func TestHasGoosePrompt(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{"empty", "", false},
		{"just underscore prompt", ">_", true},
		{"just angle prompt", ">", true},
		{"prompt with trailing space", "> ", true},
		{"banner then prompt", "Welcome to Goose\n\n>_", true},
		{"busy text", "thinking...", false},
		{"random text", "Hello world", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasGoosePrompt(tt.output); got != tt.want {
				t.Errorf("HasGoosePrompt(%q) = %v, want %v", tt.output, got, tt.want)
			}
		})
	}
}

func TestHasGooseBusyIndicator(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{"empty", "", false},
		{"thinking", "thinking...", true},
		{"executing", "executing...", true},
		{"ctrl+c", "ctrl+c to interrupt", true},
		{"esc", "esc to interrupt", true},
		{"random text", "Hello world", false},
		{"prompt only", ">_", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasGooseBusyIndicator(tt.output); got != tt.want {
				t.Errorf("HasGooseBusyIndicator(%q) = %v, want %v", tt.output, got, tt.want)
			}
		})
	}
}

func TestGooseSettings_MarshalUnmarshalRoundtrip(t *testing.T) {
	orig := &GooseSettings{
		Command:   "goose --model gpt-4o",
		EnvFile:   "/tmp/goose.env",
		YoloMode:  true,
		ConfigDir: "/custom/config",
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got GooseSettings
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if !reflect.DeepEqual(orig, &got) {
		t.Errorf("roundtrip mismatch:\n  orig: %+v\n  got:  %+v", orig, got)
	}
}

func TestDefaultGooseSettings(t *testing.T) {
	settings := DefaultGooseSettings()
	if settings == nil {
		t.Fatal("DefaultGooseSettings() returned nil")
	}
	if settings.Command != "goose" {
		t.Errorf("DefaultGooseSettings().Command = %q, want %q", settings.Command, "goose")
	}
	if settings.ConfigDir == "" {
		t.Error("DefaultGooseSettings().ConfigDir should not be empty")
	}
}

func TestIsClaudeCompatible_GooseNotCompatible(t *testing.T) {
	if IsClaudeCompatible("goose") {
		t.Error("IsClaudeCompatible(\"goose\") must be false")
	}
}

func TestGetToolIcon_Goose(t *testing.T) {
	icon := GetToolIcon("goose")
	if icon == "" {
		t.Error("GetToolIcon(\"goose\") returned empty")
	}
	if icon == GetToolIcon("shell") {
		t.Errorf("GetToolIcon(\"goose\") = %q equals shell fallback (want a distinct icon)", icon)
	}
}

func TestNewInstanceWithTool_Goose(t *testing.T) {
	inst := NewInstanceWithTool("goose-test", "/tmp/goose-test-proj", "goose")
	if inst == nil {
		t.Fatal("NewInstanceWithTool returned nil")
	}
	if inst.Tool != "goose" {
		t.Errorf("inst.Tool = %q, want %q", inst.Tool, "goose")
	}
}
