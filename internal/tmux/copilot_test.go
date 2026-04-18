package tmux

import "testing"

// Issue #556: tmux-layer detection + pattern defaults for GitHub Copilot CLI.

func TestDetectToolFromCommand_Copilot(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{"bare copilot", "copilot", "copilot"},
		{"copilot with resume flag", "copilot --resume", "copilot"},
		{"copilot via npx", "npx @github/copilot", "copilot"},
		{"uppercase binary", "COPILOT", "copilot"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectToolFromCommand(tt.command); got != tt.want {
				t.Fatalf("detectToolFromCommand(%q) = %q, want %q", tt.command, got, tt.want)
			}
		})
	}
}

func TestDefaultRawPatterns_Copilot(t *testing.T) {
	raw := DefaultRawPatterns("copilot")
	if raw == nil {
		t.Fatal("expected non-nil RawPatterns for copilot")
	}
	if len(raw.BusyPatterns) == 0 {
		t.Error("copilot should have busy patterns")
	}
	if len(raw.PromptPatterns) == 0 {
		t.Error("copilot should have prompt patterns")
	}
}
