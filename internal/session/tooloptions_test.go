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
			name: "teammate mode only",
			opts: ClaudeOptions{
				UseTeammateMode: true,
			},
			expected: []string{"--teammate-mode", "tmux"},
		},
		{
			name: "all flags",
			opts: ClaudeOptions{
				SessionMode:     "continue",
				SkipPermissions: true,
				UseChrome:       true,
				UseTeammateMode: true,
			},
			expected: []string{"-c", "--dangerously-skip-permissions", "--chrome", "--teammate-mode", "tmux"},
		},
		{
			name: "allow skip permissions only",
			opts: ClaudeOptions{
				AllowSkipPermissions: true,
			},
			expected: []string{"--allow-dangerously-skip-permissions"},
		},
		{
			name: "skip permissions takes precedence over allow",
			opts: ClaudeOptions{
				SkipPermissions:      true,
				AllowSkipPermissions: true,
			},
			expected: []string{"--dangerously-skip-permissions"},
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
			name: "teammate mode",
			opts: ClaudeOptions{
				UseTeammateMode: true,
			},
			expected: []string{"--teammate-mode", "tmux"},
		},
		{
			name: "all flags",
			opts: ClaudeOptions{
				SkipPermissions: true,
				UseChrome:       true,
				UseTeammateMode: true,
			},
			expected: []string{"--dangerously-skip-permissions", "--chrome", "--teammate-mode", "tmux"},
		},
		{
			name: "allow skip permissions for fork",
			opts: ClaudeOptions{
				AllowSkipPermissions: true,
			},
			expected: []string{"--allow-dangerously-skip-permissions"},
		},
		{
			name: "skip permissions takes precedence for fork",
			opts: ClaudeOptions{
				SkipPermissions:      true,
				AllowSkipPermissions: true,
			},
			expected: []string{"--dangerously-skip-permissions"},
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
	dangerousModeBool := true
	config := &UserConfig{
		Claude: ClaudeSettings{
			DangerousMode: &dangerousModeBool,
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
	if opts.AllowSkipPermissions {
		t.Error("expected AllowSkipPermissions=false when config is nil")
	}
}

func TestNewClaudeOptions_AllowDangerousMode(t *testing.T) {
	dangerousModeFalse := false
	config := &UserConfig{
		Claude: ClaudeSettings{
			DangerousMode:      &dangerousModeFalse,
			AllowDangerousMode: true,
		},
	}

	opts := NewClaudeOptions(config)

	if opts.SkipPermissions {
		t.Error("expected SkipPermissions=false when dangerous_mode=false")
	}
	if !opts.AllowSkipPermissions {
		t.Error("expected AllowSkipPermissions=true when allow_dangerous_mode=true")
	}
}

func TestNewClaudeOptions_DefaultDangerousMode(t *testing.T) {
	// With nil DangerousMode (default=true), SkipPermissions should be true
	config := &UserConfig{
		Claude: ClaudeSettings{},
	}

	opts := NewClaudeOptions(config)

	if !opts.SkipPermissions {
		t.Error("expected SkipPermissions=true when dangerous_mode is nil (default=true)")
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
		UseTeammateMode: true,
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
	if !result.UseTeammateMode {
		t.Error("expected UseTeammateMode=true")
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

// === Codex Options Tests ===

func TestCodexOptions_ToolName(t *testing.T) {
	opts := &CodexOptions{}
	if opts.ToolName() != "codex" {
		t.Errorf("expected ToolName() = 'codex', got %q", opts.ToolName())
	}
}

func TestCodexOptions_ToArgs(t *testing.T) {
	tests := []struct {
		name     string
		opts     CodexOptions
		expected []string
	}{
		{
			name:     "yolo nil (inherit)",
			opts:     CodexOptions{YoloMode: nil},
			expected: nil,
		},
		{
			name:     "yolo true",
			opts:     CodexOptions{YoloMode: boolPtr(true)},
			expected: []string{"--yolo"},
		},
		{
			name:     "yolo false",
			opts:     CodexOptions{YoloMode: boolPtr(false)},
			expected: nil,
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

func TestNewCodexOptions_WithConfig(t *testing.T) {
	// Global yolo=true
	config := &UserConfig{
		Codex: CodexSettings{YoloMode: true},
	}
	opts := NewCodexOptions(config)
	if opts.YoloMode == nil || !*opts.YoloMode {
		t.Error("expected YoloMode=true when config.Codex.YoloMode=true")
	}

	// Global yolo=false
	config2 := &UserConfig{
		Codex: CodexSettings{YoloMode: false},
	}
	opts2 := NewCodexOptions(config2)
	if opts2.YoloMode != nil {
		t.Errorf("expected YoloMode=nil when config.Codex.YoloMode=false, got %v", *opts2.YoloMode)
	}
}

func TestNewCodexOptions_NilConfig(t *testing.T) {
	opts := NewCodexOptions(nil)
	if opts.YoloMode != nil {
		t.Errorf("expected YoloMode=nil when config is nil, got %v", *opts.YoloMode)
	}
}

func TestCodexOptions_MarshalUnmarshal(t *testing.T) {
	original := &CodexOptions{YoloMode: boolPtr(true)}

	data, err := MarshalToolOptions(original)
	if err != nil {
		t.Fatalf("MarshalToolOptions failed: %v", err)
	}

	restored, err := UnmarshalCodexOptions(data)
	if err != nil {
		t.Fatalf("UnmarshalCodexOptions failed: %v", err)
	}

	if restored.YoloMode == nil || !*restored.YoloMode {
		t.Error("expected YoloMode=true after roundtrip")
	}
}

func TestUnmarshalCodexOptions_EmptyData(t *testing.T) {
	result, err := UnmarshalCodexOptions(nil)
	if err != nil {
		t.Fatalf("UnmarshalCodexOptions(nil) failed: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for empty data, got %v", result)
	}
}

func TestUnmarshalCodexOptions_WrongTool(t *testing.T) {
	// Create JSON with claude tool name — should return nil for codex
	claudeOpts := &ClaudeOptions{SkipPermissions: true}
	data, _ := MarshalToolOptions(claudeOpts)

	result, err := UnmarshalCodexOptions(data)
	if err != nil {
		t.Fatalf("UnmarshalCodexOptions failed: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for wrong tool, got %v", result)
	}
}

func TestCodexOptions_RoundTrip_NilYolo(t *testing.T) {
	original := &CodexOptions{YoloMode: nil}

	data, err := MarshalToolOptions(original)
	if err != nil {
		t.Fatalf("MarshalToolOptions failed: %v", err)
	}

	restored, err := UnmarshalCodexOptions(data)
	if err != nil {
		t.Fatalf("UnmarshalCodexOptions failed: %v", err)
	}

	if restored.YoloMode != nil {
		t.Errorf("expected YoloMode=nil after roundtrip, got %v", *restored.YoloMode)
	}
}

func TestClaudeOptions_RoundTrip_AllowSkipPermissions(t *testing.T) {
	original := &ClaudeOptions{
		SessionMode:          "new",
		AllowSkipPermissions: true,
	}

	data, err := MarshalToolOptions(original)
	if err != nil {
		t.Fatalf("MarshalToolOptions failed: %v", err)
	}

	restored, err := UnmarshalClaudeOptions(data)
	if err != nil {
		t.Fatalf("UnmarshalClaudeOptions failed: %v", err)
	}

	if !restored.AllowSkipPermissions {
		t.Error("expected AllowSkipPermissions=true after roundtrip")
	}
	if restored.SkipPermissions {
		t.Error("expected SkipPermissions=false after roundtrip")
	}
}

// === OpenCode Options Tests ===

func TestOpenCodeOptions_ToolName(t *testing.T) {
	opts := &OpenCodeOptions{}
	if opts.ToolName() != "opencode" {
		t.Errorf("expected ToolName() = 'opencode', got %q", opts.ToolName())
	}
}

func TestOpenCodeOptions_ToArgs(t *testing.T) {
	tests := []struct {
		name     string
		opts     OpenCodeOptions
		expected []string
	}{
		{
			name:     "empty options",
			opts:     OpenCodeOptions{},
			expected: nil,
		},
		{
			name:     "new session mode (default)",
			opts:     OpenCodeOptions{SessionMode: "new"},
			expected: nil,
		},
		{
			name:     "continue mode",
			opts:     OpenCodeOptions{SessionMode: "continue"},
			expected: []string{"-c"},
		},
		{
			name: "resume mode with session ID",
			opts: OpenCodeOptions{
				SessionMode:     "resume",
				ResumeSessionID: "ses_abc123",
			},
			expected: []string{"-s", "ses_abc123"},
		},
		{
			name:     "resume mode without session ID",
			opts:     OpenCodeOptions{SessionMode: "resume"},
			expected: nil,
		},
		{
			name:     "model only",
			opts:     OpenCodeOptions{Model: "anthropic/claude-sonnet-4-5-20250929"},
			expected: []string{"-m", "anthropic/claude-sonnet-4-5-20250929"},
		},
		{
			name:     "agent only",
			opts:     OpenCodeOptions{Agent: "coder"},
			expected: []string{"--agent", "coder"},
		},
		{
			name: "all flags",
			opts: OpenCodeOptions{
				SessionMode: "continue",
				Model:       "openai/gpt-4o",
				Agent:       "reviewer",
			},
			expected: []string{"-c", "-m", "openai/gpt-4o", "--agent", "reviewer"},
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

func TestOpenCodeOptions_ToArgsForFork(t *testing.T) {
	tests := []struct {
		name     string
		opts     OpenCodeOptions
		expected []string
	}{
		{
			name:     "empty options",
			opts:     OpenCodeOptions{},
			expected: nil,
		},
		{
			name: "session mode ignored for fork",
			opts: OpenCodeOptions{
				SessionMode:     "resume",
				ResumeSessionID: "ses_abc123",
			},
			expected: nil,
		},
		{
			name: "model and agent preserved for fork",
			opts: OpenCodeOptions{
				SessionMode: "continue",
				Model:       "anthropic/claude-sonnet-4-5-20250929",
				Agent:       "coder",
			},
			expected: []string{"-m", "anthropic/claude-sonnet-4-5-20250929", "--agent", "coder"},
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

func TestNewOpenCodeOptions_WithConfig(t *testing.T) {
	config := &UserConfig{
		OpenCode: OpenCodeSettings{
			DefaultModel: "anthropic/claude-sonnet-4-5-20250929",
			DefaultAgent: "coder",
		},
	}
	opts := NewOpenCodeOptions(config)

	if opts.SessionMode != "new" {
		t.Errorf("expected SessionMode='new', got %q", opts.SessionMode)
	}
	if opts.Model != "anthropic/claude-sonnet-4-5-20250929" {
		t.Errorf("expected Model from config, got %q", opts.Model)
	}
	if opts.Agent != "coder" {
		t.Errorf("expected Agent from config, got %q", opts.Agent)
	}
}

func TestNewOpenCodeOptions_NilConfig(t *testing.T) {
	opts := NewOpenCodeOptions(nil)

	if opts.SessionMode != "new" {
		t.Errorf("expected SessionMode='new', got %q", opts.SessionMode)
	}
	if opts.Model != "" {
		t.Errorf("expected empty Model, got %q", opts.Model)
	}
	if opts.Agent != "" {
		t.Errorf("expected empty Agent, got %q", opts.Agent)
	}
}

func TestOpenCodeOptions_MarshalUnmarshal(t *testing.T) {
	original := &OpenCodeOptions{
		SessionMode:     "resume",
		ResumeSessionID: "ses_test123",
		Model:           "openai/gpt-4o",
		Agent:           "reviewer",
	}

	data, err := MarshalToolOptions(original)
	if err != nil {
		t.Fatalf("MarshalToolOptions failed: %v", err)
	}

	restored, err := UnmarshalOpenCodeOptions(data)
	if err != nil {
		t.Fatalf("UnmarshalOpenCodeOptions failed: %v", err)
	}

	if !reflect.DeepEqual(original, restored) {
		t.Errorf("round-trip failed: original=%+v, restored=%+v", original, restored)
	}
}

func TestUnmarshalOpenCodeOptions_EmptyData(t *testing.T) {
	result, err := UnmarshalOpenCodeOptions(nil)
	if err != nil {
		t.Fatalf("UnmarshalOpenCodeOptions(nil) failed: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for empty data, got %v", result)
	}
}

func TestUnmarshalOpenCodeOptions_WrongTool(t *testing.T) {
	claudeOpts := &ClaudeOptions{SkipPermissions: true}
	data, _ := MarshalToolOptions(claudeOpts)

	result, err := UnmarshalOpenCodeOptions(data)
	if err != nil {
		t.Fatalf("UnmarshalOpenCodeOptions failed: %v", err)
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
		UseTeammateMode: true,
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
