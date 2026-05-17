package session

import "testing"

func TestParseModelID(t *testing.T) {
	tests := []struct {
		name    string
		modelID string
		model   string
		version string
		display string
	}{
		{
			name:    "claude dateless id",
			modelID: "claude-sonnet-4-6",
			model:   "Claude Sonnet",
			version: "4.6",
			display: "Claude Sonnet 4.6",
		},
		{
			name:    "claude dated id",
			modelID: "claude-haiku-4-5-20251001",
			model:   "Claude Haiku",
			version: "4.5 20251001",
			display: "Claude Haiku 4.5 20251001",
		},
		{
			name:    "opencode anthropic provider",
			modelID: "anthropic/claude-opus-4-7",
			model:   "Anthropic Claude Opus",
			version: "4.7",
			display: "Anthropic Claude Opus 4.7",
		},
		{
			name:    "gpt pro",
			modelID: "gpt-5.5-pro",
			model:   "GPT Pro",
			version: "5.5",
			display: "GPT Pro 5.5",
		},
		{
			name:    "codex optimized gpt",
			modelID: "gpt-5.3-codex",
			model:   "GPT Codex",
			version: "5.3",
			display: "GPT Codex 5.3",
		},
		{
			name:    "gemini preview",
			modelID: "gemini-3.1-pro-preview",
			model:   "Gemini Pro",
			version: "3.1 Preview",
			display: "Gemini Pro 3.1 Preview",
		},
		{
			name:    "gemini preview custom tools",
			modelID: "gemini-3.1-pro-preview-customtools",
			model:   "Gemini Pro Customtools",
			version: "3.1 Preview",
			display: "Gemini Pro Customtools 3.1 Preview",
		},
		{
			name:    "gemini flash lite",
			modelID: "gemini-2.5-flash-lite",
			model:   "Gemini Flash Lite",
			version: "2.5",
			display: "Gemini Flash Lite 2.5",
		},
		{
			name:    "openai provider",
			modelID: "openai/gpt-5.4-mini",
			model:   "OpenAI GPT Mini",
			version: "5.4",
			display: "OpenAI GPT Mini 5.4",
		},
		{
			name:    "openai reasoning",
			modelID: "o3-pro",
			model:   "OpenAI Reasoning",
			version: "o3-pro",
			display: "OpenAI Reasoning o3-pro",
		},
		{
			name:    "custom fallback",
			modelID: "provider/custom-model",
			model:   "custom-model",
			version: "",
			display: "custom-model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseModelID(tt.modelID)
			if got.ModelID != tt.modelID {
				t.Fatalf("ModelID = %q, want %q", got.ModelID, tt.modelID)
			}
			if got.Model != tt.model {
				t.Fatalf("Model = %q, want %q", got.Model, tt.model)
			}
			if got.Version != tt.version {
				t.Fatalf("Version = %q, want %q", got.Version, tt.version)
			}
			if got.Display() != tt.display {
				t.Fatalf("Display() = %q, want %q", got.Display(), tt.display)
			}
		})
	}
}

func TestInstanceLaunchModelInfo(t *testing.T) {
	tests := []struct {
		name    string
		inst    *Instance
		modelID string
	}{
		{
			name: "claude options",
			inst: func() *Instance {
				inst := NewInstanceWithTool("claude", "/tmp/test", "claude")
				if err := inst.SetClaudeOptions(&ClaudeOptions{Model: "claude-sonnet-4-6"}); err != nil {
					t.Fatal(err)
				}
				return inst
			}(),
			modelID: "claude-sonnet-4-6",
		},
		{
			name: "codex options",
			inst: func() *Instance {
				inst := NewInstanceWithTool("codex", "/tmp/test", "codex")
				if err := inst.SetCodexOptions(&CodexOptions{Model: "gpt-5.5"}); err != nil {
					t.Fatal(err)
				}
				return inst
			}(),
			modelID: "gpt-5.5",
		},
		{
			name: "gemini field",
			inst: func() *Instance {
				inst := NewInstanceWithTool("gemini", "/tmp/test", "gemini")
				inst.GeminiModel = "gemini-3.1-pro-preview"
				return inst
			}(),
			modelID: "gemini-3.1-pro-preview",
		},
		{
			name: "opencode options",
			inst: func() *Instance {
				inst := NewInstanceWithTool("opencode", "/tmp/test", "opencode")
				if err := inst.SetOpenCodeOptions(&OpenCodeOptions{Model: "openai/gpt-5.4-mini"}); err != nil {
					t.Fatal(err)
				}
				return inst
			}(),
			modelID: "openai/gpt-5.4-mini",
		},
		{
			name:    "tool default",
			inst:    NewInstanceWithTool("shell", "/tmp/test", "shell"),
			modelID: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.inst.LaunchModelInfo()
			if got.ModelID != tt.modelID {
				t.Fatalf("LaunchModelInfo().ModelID = %q, want %q", got.ModelID, tt.modelID)
			}
		})
	}
}
