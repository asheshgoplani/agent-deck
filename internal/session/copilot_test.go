package session

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Issue #556: GitHub Copilot CLI support.
// These tests define the Tool="copilot" contract: options marshalling,
// ToArgs for new/resume, factory from config, and the basic identity gates
// (icon, IsClaudeCompatible, builtin-name filter).
//
// Model: https://docs.github.com/en/copilot/concepts/agents/about-copilot-cli
// Binary: `copilot` (npm @github/copilot), interactive REPL with --resume
// picker for prior sessions.

func TestCopilotOptions_ToolName(t *testing.T) {
	opts := &CopilotOptions{}
	if got := opts.ToolName(); got != "copilot" {
		t.Errorf("CopilotOptions.ToolName() = %q, want %q", got, "copilot")
	}
}

func TestCopilotOptions_ToArgs(t *testing.T) {
	tests := []struct {
		name     string
		opts     CopilotOptions
		expected []string
	}{
		{
			name:     "default new session - no args",
			opts:     CopilotOptions{SessionMode: "new"},
			expected: nil,
		},
		{
			name:     "empty session mode - no args",
			opts:     CopilotOptions{},
			expected: nil,
		},
		{
			name:     "resume without id - picker mode",
			opts:     CopilotOptions{SessionMode: "resume"},
			expected: []string{"--resume"},
		},
		{
			name:     "resume with id",
			opts:     CopilotOptions{SessionMode: "resume", ResumeSessionID: "abc123"},
			expected: []string{"--resume", "abc123"},
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

func TestNewCopilotOptions_Defaults(t *testing.T) {
	opts := NewCopilotOptions(nil)
	if opts == nil {
		t.Fatal("NewCopilotOptions(nil) returned nil")
	}
	if opts.SessionMode != "new" {
		t.Errorf("default SessionMode = %q, want %q", opts.SessionMode, "new")
	}
}

func TestNewCopilotOptions_WithConfig(t *testing.T) {
	cfg := &UserConfig{
		Copilot: CopilotSettings{EnvFile: "/tmp/copilot.env"},
	}
	opts := NewCopilotOptions(cfg)
	if opts == nil {
		t.Fatal("NewCopilotOptions returned nil")
	}
	if opts.SessionMode != "new" {
		t.Errorf("SessionMode = %q, want %q", opts.SessionMode, "new")
	}
}

func TestCopilotOptions_MarshalUnmarshalRoundtrip(t *testing.T) {
	orig := &CopilotOptions{SessionMode: "resume", ResumeSessionID: "sess-42"}

	data, err := MarshalToolOptions(orig)
	if err != nil {
		t.Fatalf("MarshalToolOptions: %v", err)
	}

	var wrapper ToolOptionsWrapper
	if err := json.Unmarshal(data, &wrapper); err != nil {
		t.Fatalf("unmarshal wrapper: %v", err)
	}
	if wrapper.Tool != "copilot" {
		t.Errorf("wrapper.Tool = %q, want %q", wrapper.Tool, "copilot")
	}

	got, err := UnmarshalCopilotOptions(data)
	if err != nil {
		t.Fatalf("UnmarshalCopilotOptions: %v", err)
	}
	if got == nil {
		t.Fatal("UnmarshalCopilotOptions returned nil")
	}
	if got.SessionMode != "resume" || got.ResumeSessionID != "sess-42" {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
}

func TestUnmarshalCopilotOptions_WrongTool(t *testing.T) {
	raw := json.RawMessage(`{"tool":"codex","options":{}}`)
	got, err := UnmarshalCopilotOptions(raw)
	if err != nil {
		t.Fatalf("UnmarshalCopilotOptions: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for wrong tool, got %+v", got)
	}
}

func TestIsClaudeCompatible_CopilotNotCompatible(t *testing.T) {
	if IsClaudeCompatible("copilot") {
		t.Error("IsClaudeCompatible(\"copilot\") must be false")
	}
}

func TestGetToolIcon_Copilot(t *testing.T) {
	icon := GetToolIcon("copilot")
	if icon == "" {
		t.Error("GetToolIcon(\"copilot\") returned empty")
	}
	if icon == GetToolIcon("shell") {
		t.Errorf("GetToolIcon(\"copilot\") = %q equals shell fallback (want a distinct icon)", icon)
	}
}

func TestGetCustomToolNames_CopilotIsBuiltin(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()

	userConfigCache = &UserConfig{
		Tools: map[string]ToolDef{
			"copilot":    {Command: "copilot"},
			"my-wrapper": {Command: "claude"},
		},
	}

	names := GetCustomToolNames()
	for _, n := range names {
		if n == "copilot" {
			t.Errorf("GetCustomToolNames() returned %q as custom; copilot is built-in", n)
		}
	}
}

func TestNewInstanceWithTool_Copilot(t *testing.T) {
	inst := NewInstanceWithTool("copilot-test", "/tmp/copilot-test-proj", "copilot")
	if inst == nil {
		t.Fatal("NewInstanceWithTool returned nil")
	}
	if inst.Tool != "copilot" {
		t.Errorf("inst.Tool = %q, want %q", inst.Tool, "copilot")
	}
}
