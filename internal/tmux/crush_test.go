package tmux

import "testing"

// Issue #940: charmbracelet/crush — tmux-layer detection tests.

func TestDetectToolFromCommand_Crush(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{"bare crush", "crush", "crush"},
		{"crush with yolo flag", "crush --yolo", "crush"},
		{"crush absolute path", "/usr/local/bin/crush", "crush"},
		{"crush home path", "/home/user/.local/bin/crush", "crush"},
		{"uppercase binary", "CRUSH", "crush"},
		{"crush with session arg", "crush --session abc123", "crush"},
		{"crush continue", "crush --continue", "crush"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectToolFromCommand(tt.command); got != tt.want {
				t.Fatalf("detectToolFromCommand(%q) = %q, want %q", tt.command, got, tt.want)
			}
		})
	}
}

func TestDetectToolFromCommand_Crush_Negative(t *testing.T) {
	tests := []struct {
		name    string
		command string
	}{
		// Truly unrelated strings — strings.Contains fallback is intentionally
		// permissive (matches the copilot / hermes precedent), so we only
		// guard against blatant false positives here.
		{"empty", ""},
		{"unrelated tool", "ls -la"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectToolFromCommand(tt.command)
			if got == "crush" {
				t.Fatalf("detectToolFromCommand(%q) = %q, should NOT match crush", tt.command, got)
			}
		})
	}
}

func TestDetectToolFromContent_Crush(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"charm crush banner", "Welcome to Charm Crush", "crush"},
		{"crush prompt", "> what should I do? crush>", "crush"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectToolFromContent(tt.content); got != tt.want {
				t.Fatalf("detectToolFromContent(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}
