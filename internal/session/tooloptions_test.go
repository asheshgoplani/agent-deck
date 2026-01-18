package session

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestClaudeOptions_ToolName(t *testing.T) {
	opts := &ClaudeOptions{}
	if opts.ToolName() != "claude" {
		t.Errorf("expected ToolName() = 'claude', got %q", opts.ToolName())
	}
}

func TestClaudeOptions_ToArgs(t *testing.T) {
	tests := []struct {
		name     string
		opts     ClaudeOptions
		expected []string
	}{
		{
			name:     "empty options",
			opts:     ClaudeOptions{},
			expected: nil,
		},
		{
			name: "new session mode (default)",
			opts: ClaudeOptions{
				SessionMode: "new",
			},
			expected: nil,
		},
		{
			name: "continue mode",
			opts: ClaudeOptions{
				SessionMode: "continue",
			},
			expected: []string{"-c"},
		},
		{
			name: "resume mode with session ID",
			opts: ClaudeOptions{
				SessionMode:     "resume",
				ResumeSessionID: "abc-123",
			},
			expected: []string{"--resume", "abc-123"},
		},
		{
			name: "resume mode without session ID",
			opts: ClaudeOptions{
				SessionMode: "resume",
			},
			expected: nil,
		},
		{
			name: "skip permissions only",
			opts: ClaudeOptions{
				SkipPermissions: true,
			},
			expected: []string{"--dangerously-skip-permissions"},
		},
		{
			name: "chrome only",
			opts: ClaudeOptions{
				UseChrome: true,
			},
			expected: []string{"--chrome"},
		},
		{
			name: "all flags",
			opts: ClaudeOptions{
				SessionMode:     "continue",
				SkipPermissions: true,
				UseChrome:       true,
			},
			expected: []string{"-c", "--dangerously-skip-permissions", "--chrome"},
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

func TestClaudeOptions_ToArgsForFork(t *testing.T) {
	tests := []struct {
		name     string
		opts     ClaudeOptions
		expected []string
	}{
		{
			name:     "empty options",
			opts:     ClaudeOptions{},
			expected: nil,
		},
		{
			name: "session mode ignored for fork",
			opts: ClaudeOptions{
				SessionMode: "continue",
			},
			expected: nil,
		},
		{
			name: "skip permissions",
			opts: ClaudeOptions{
				SkipPermissions: true,
			},
			expected: []string{"--dangerously-skip-permissions"},
		},
		{
			name: "chrome",
			opts: ClaudeOptions{
				UseChrome: true,
			},
			expected: []string{"--chrome"},
		},
		{
			name: "both flags",
			opts: ClaudeOptions{
				SkipPermissions: true,
				UseChrome:       true,
			},
			expected: []string{"--dangerously-skip-permissions", "--chrome"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.opts.ToArgsForFork()
			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("ToArgsForFork() = %v, expected %v", got, tt.expected)
			}
		})
	}
}

func TestNewClaudeOptions_WithConfig(t *testing.T) {
	// Test with dangerous mode enabled in config
	config := &UserConfig{
		Claude: ClaudeSettings{
			DangerousMode: true,
		},
	}

	opts := NewClaudeOptions(config)

	if opts.SessionMode != "new" {
		t.Errorf("expected SessionMode='new', got %q", opts.SessionMode)
	}
	if !opts.SkipPermissions {
		t.Error("expected SkipPermissions=true when config.DangerousMode=true")
	}
}

func TestNewClaudeOptions_NilConfig(t *testing.T) {
	opts := NewClaudeOptions(nil)

	if opts.SessionMode != "new" {
		t.Errorf("expected SessionMode='new', got %q", opts.SessionMode)
	}
	if opts.SkipPermissions {
		t.Error("expected SkipPermissions=false when config is nil")
	}
}

func TestMarshalToolOptions(t *testing.T) {
	opts := &ClaudeOptions{
		SessionMode:     "continue",
		SkipPermissions: true,
		UseChrome:       false,
	}

	data, err := MarshalToolOptions(opts)
	if err != nil {
		t.Fatalf("MarshalToolOptions failed: %v", err)
	}

	// Parse the result to verify structure
	var wrapper ToolOptionsWrapper
	if err := json.Unmarshal(data, &wrapper); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	if wrapper.Tool != "claude" {
		t.Errorf("expected tool='claude', got %q", wrapper.Tool)
	}

	// Verify inner options
	var innerOpts ClaudeOptions
	if err := json.Unmarshal(wrapper.Options, &innerOpts); err != nil {
		t.Fatalf("failed to unmarshal inner options: %v", err)
	}

	if innerOpts.SessionMode != "continue" {
		t.Errorf("expected SessionMode='continue', got %q", innerOpts.SessionMode)
	}
	if !innerOpts.SkipPermissions {
		t.Error("expected SkipPermissions=true")
	}
}

func TestMarshalToolOptions_Nil(t *testing.T) {
	data, err := MarshalToolOptions(nil)
	if err != nil {
		t.Fatalf("MarshalToolOptions(nil) failed: %v", err)
	}
	if data != nil {
		t.Errorf("expected nil for nil input, got %v", data)
	}
}

func TestUnmarshalClaudeOptions(t *testing.T) {
	// Create valid wrapped JSON
	opts := &ClaudeOptions{
		SessionMode:     "resume",
		ResumeSessionID: "test-session-123",
		SkipPermissions: true,
		UseChrome:       true,
	}

	data, err := MarshalToolOptions(opts)
	if err != nil {
		t.Fatalf("MarshalToolOptions failed: %v", err)
	}

	// Unmarshal back
	result, err := UnmarshalClaudeOptions(data)
	if err != nil {
		t.Fatalf("UnmarshalClaudeOptions failed: %v", err)
	}

	if result.SessionMode != "resume" {
		t.Errorf("expected SessionMode='resume', got %q", result.SessionMode)
	}
	if result.ResumeSessionID != "test-session-123" {
		t.Errorf("expected ResumeSessionID='test-session-123', got %q", result.ResumeSessionID)
	}
	if !result.SkipPermissions {
		t.Error("expected SkipPermissions=true")
	}
	if !result.UseChrome {
		t.Error("expected UseChrome=true")
	}
}

func TestUnmarshalClaudeOptions_EmptyData(t *testing.T) {
	result, err := UnmarshalClaudeOptions(nil)
	if err != nil {
		t.Fatalf("UnmarshalClaudeOptions(nil) failed: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for empty data, got %v", result)
	}

	result, err = UnmarshalClaudeOptions([]byte{})
	if err != nil {
		t.Fatalf("UnmarshalClaudeOptions([]) failed: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for empty slice, got %v", result)
	}
}

func TestUnmarshalClaudeOptions_WrongTool(t *testing.T) {
	// Create JSON with different tool name
	wrapper := ToolOptionsWrapper{
		Tool:    "gemini",
		Options: []byte(`{}`),
	}
	data, _ := json.Marshal(wrapper)

	result, err := UnmarshalClaudeOptions(data)
	if err != nil {
		t.Fatalf("UnmarshalClaudeOptions failed: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for wrong tool, got %v", result)
	}
}

func TestClaudeOptions_RoundTrip(t *testing.T) {
	// Test complete round-trip serialization
	original := &ClaudeOptions{
		SessionMode:     "resume",
		ResumeSessionID: "session-abc-123",
		SkipPermissions: true,
		UseChrome:       true,
	}

	// Marshal
	data, err := MarshalToolOptions(original)
	if err != nil {
		t.Fatalf("MarshalToolOptions failed: %v", err)
	}

	// Unmarshal
	restored, err := UnmarshalClaudeOptions(data)
	if err != nil {
		t.Fatalf("UnmarshalClaudeOptions failed: %v", err)
	}

	// Compare
	if !reflect.DeepEqual(original, restored) {
		t.Errorf("round-trip failed: original=%+v, restored=%+v", original, restored)
	}
}
