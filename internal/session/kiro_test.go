package session

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestKiroOptions_ToolName(t *testing.T) {
	opts := &KiroOptions{}
	if got := opts.ToolName(); got != "kiro" {
		t.Errorf("KiroOptions.ToolName() = %q, want %q", got, "kiro")
	}
}

func TestKiroOptions_ToArgs(t *testing.T) {
	tests := []struct {
		name string
		opts KiroOptions
		want []string
	}{
		{"empty", KiroOptions{}, nil},
		{"resume latest", KiroOptions{SessionMode: "resume"}, []string{"--resume"}},
		{"resume by id", KiroOptions{SessionMode: "resume", ResumeSessionID: "abc-123"}, []string{"--resume-id", "abc-123"}},
		{"trust all tools", KiroOptions{TrustAllTools: true}, []string{"--trust-all-tools"}},
		{"agent", KiroOptions{Agent: "my-agent"}, []string{"--agent", "my-agent"}},
		{"model", KiroOptions{Model: "claude-sonnet-4.6"}, []string{"--model", "claude-sonnet-4.6"}},
		{"combined", KiroOptions{SessionMode: "resume", ResumeSessionID: "x", TrustAllTools: true, Agent: "a"}, []string{"--resume-id", "x", "--agent", "a", "--trust-all-tools"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.opts.ToArgs()
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Errorf("ToArgs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestKiroOptions_MarshalUnmarshal(t *testing.T) {
	opts := &KiroOptions{
		SessionMode:     "resume",
		ResumeSessionID: "test-uuid-123",
		Agent:           "kiro_planner",
		TrustAllTools:   true,
	}

	data, err := MarshalToolOptions(opts)
	if err != nil {
		t.Fatalf("MarshalToolOptions: %v", err)
	}

	got, err := UnmarshalKiroOptions(data)
	if err != nil {
		t.Fatalf("UnmarshalKiroOptions: %v", err)
	}
	if got == nil {
		t.Fatal("UnmarshalKiroOptions returned nil")
	}
	if got.SessionMode != opts.SessionMode {
		t.Errorf("SessionMode = %q, want %q", got.SessionMode, opts.SessionMode)
	}
	if got.ResumeSessionID != opts.ResumeSessionID {
		t.Errorf("ResumeSessionID = %q, want %q", got.ResumeSessionID, opts.ResumeSessionID)
	}
	if got.Agent != opts.Agent {
		t.Errorf("Agent = %q, want %q", got.Agent, opts.Agent)
	}
	if got.TrustAllTools != opts.TrustAllTools {
		t.Errorf("TrustAllTools = %v, want %v", got.TrustAllTools, opts.TrustAllTools)
	}
}

func TestKiroOptions_UnmarshalWrongTool(t *testing.T) {
	data := json.RawMessage(`{"tool":"claude","options":{}}`)
	got, err := UnmarshalKiroOptions(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for wrong tool, got %+v", got)
	}
}

func TestBuildKiroCommand(t *testing.T) {
	inst := NewInstanceWithTool("test-kiro", "/tmp/project", "kiro")

	cmd := inst.buildKiroCommand("kiro-cli")
	if !strings.Contains(cmd, "kiro-cli chat") {
		t.Errorf("buildKiroCommand should contain 'kiro-cli chat', got %q", cmd)
	}
}

func TestBuildKiroCommand_WithSessionID(t *testing.T) {
	inst := NewInstanceWithTool("test-kiro", "/tmp/project", "kiro")
	inst.KiroSessionID = "abc-def-123"

	cmd := inst.buildKiroCommand("kiro-cli")
	if !strings.Contains(cmd, "--resume-id abc-def-123") {
		t.Errorf("buildKiroCommand should contain '--resume-id abc-def-123', got %q", cmd)
	}
}

func TestBuildKiroCommand_WithOptions(t *testing.T) {
	inst := NewInstanceWithTool("test-kiro", "/tmp/project", "kiro")
	opts := &KiroOptions{
		TrustAllTools: true,
		Agent:         "my-agent",
		Model:         "claude-opus-4.7",
	}
	data, _ := MarshalToolOptions(opts)
	inst.ToolOptionsJSON = data

	cmd := inst.buildKiroCommand("kiro-cli")
	if !strings.Contains(cmd, "--trust-all-tools") {
		t.Errorf("expected --trust-all-tools in %q", cmd)
	}
	if !strings.Contains(cmd, "--agent my-agent") {
		t.Errorf("expected --agent my-agent in %q", cmd)
	}
	if !strings.Contains(cmd, "--model claude-opus-4.7") {
		t.Errorf("expected --model claude-opus-4.7 in %q", cmd)
	}
}
