package tmux

import "testing"

func TestDetectToolFromCommand_Kiro(t *testing.T) {
	tests := []struct {
		command string
		want    string
	}{
		{"kiro-cli", "kiro"},
		{"kiro-cli chat", "kiro"},
		{"kiro-cli chat --resume", "kiro"},
		{"kiro-cli chat --trust-all-tools", "kiro"},
		{"/home/user/.local/bin/kiro-cli chat --agent my-agent", "kiro"},
		{"kiro-cli chat --resume-id abc123", "kiro"},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			if got := detectToolFromCommand(tt.command); got != tt.want {
				t.Fatalf("detectToolFromCommand(%q) = %q, want %q", tt.command, got, tt.want)
			}
		})
	}
}

func TestDetectToolFromCommand_Kiro_Negative(t *testing.T) {
	tests := []struct {
		command string
		want    string
	}{
		{"claude", "claude"},
		{"gemini", "gemini"},
		{"bash", ""},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			got := detectToolFromCommand(tt.command)
			if got != tt.want {
				t.Fatalf("detectToolFromCommand(%q) = %q, want %q", tt.command, got, tt.want)
			}
		})
	}
}

func TestDetectToolFromContent_Kiro(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"kiro cli mention", "Welcome to Kiro CLI v2.3.0", "kiro"},
		{"kiro-cli in output", "Running kiro-cli chat session", "kiro"},
		{"unrelated content", "hello world", "shell"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectToolFromContent(tt.content); got != tt.want {
				t.Fatalf("detectToolFromContent(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

func TestPromptDetector_Kiro(t *testing.T) {
	d := NewPromptDetector("kiro")

	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"busy - esc to cancel", "Running tool...\nesc to cancel", false},
		{"busy - type to queue", "Kiro · auto · ◔ 10%\nWorking · type to queue a message", false},
		{"busy - working", "● Shell go build ./...\nWorking", false},
		{"busy - ctrl+c", "Streaming response\nctrl+c to interrupt", false},
		{"idle - enter to send", "Previous response\nEnter to send", true},
		{"permission prompt", "subagent requires approval\n ❯ Yes, single permission\n   Trust, always allow", true},
		{"busy with subagent - has queue text", "◔ Subagent researching\ntype to queue a message\nEnter to send", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := d.HasPrompt(tt.content); got != tt.want {
				t.Fatalf("HasPrompt(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}
