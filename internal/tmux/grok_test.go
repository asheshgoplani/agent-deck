package tmux

import "testing"

// xAI Grok Build CLI — tmux-layer detection tests (command + content).

func TestDetectToolFromCommand_Grok(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{"bare grok", "grok", "grok"},
		{"grok with model", "grok -m grok-build", "grok"},
		{"grok absolute path", "/Users/me/.grok/bin/grok", "grok"},
		{"uppercase binary", "GROK", "grok"},
		{"grok always-approve", "grok --always-approve", "grok"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectToolFromCommand(tt.command); got != tt.want {
				t.Fatalf("detectToolFromCommand(%q) = %q, want %q", tt.command, got, tt.want)
			}
		})
	}
}

func TestDetectToolFromCommand_Grok_Negative(t *testing.T) {
	tests := []struct {
		name    string
		command string
	}{
		{"empty", ""},
		{"unrelated tool", "ls -la"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectToolFromCommand(tt.command); got == "grok" {
				t.Fatalf("detectToolFromCommand(%q) = %q, should NOT match grok", tt.command, got)
			}
		})
	}
}

func TestDetectToolFromContent_Grok(t *testing.T) {
	// "Grok Build" is the composer border label present in the real TUI.
	if got := detectToolFromContent("╰─ Grok Build · always-approve ─╯"); got != "grok" {
		t.Fatalf("detectToolFromContent(grok banner) = %q, want %q", got, "grok")
	}
}
