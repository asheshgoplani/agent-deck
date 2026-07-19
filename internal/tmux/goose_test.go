package tmux

import "testing"

// Goose Agent CLI support — tmux-layer detection tests.

func TestDetectToolFromCommand_Goose(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{"bare goose", "goose", "goose"},
		{"goose with auto flag", "goose --auto", "goose"},
		{"goose absolute path", "/usr/local/bin/goose", "goose"},
		{"goose home path", "/home/user/.local/bin/goose", "goose"},
		{"uppercase binary", "GOOSE", "goose"},
		{"goose with model flag", "goose --model gpt-4o", "goose"},
		{"goose with profile", "goose --profile work", "goose"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectToolFromCommand(tt.command); got != tt.want {
				t.Fatalf("detectToolFromCommand(%q) = %q, want %q", tt.command, got, tt.want)
			}
		})
	}
}

func TestDetectToolFromCommand_Goose_Negative(t *testing.T) {
	tests := []struct {
		name    string
		command string
	}{
		{"empty", ""},
		{"unrelated tool", "ls -la"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectToolFromCommand(tt.command)
			if got == "goose" {
				t.Fatalf("detectToolFromCommand(%q) = %q, should NOT match goose", tt.command, got)
			}
		})
	}
}

func TestDetectToolFromContent_Goose(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"goose agent banner", "Welcome to Goose Agent", "goose"},
		{"goose prompt", "> what should I do? goose>", "goose"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectToolFromContent(tt.content); got != tt.want {
				t.Fatalf("detectToolFromContent(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

func TestDefaultRawPatterns_Goose(t *testing.T) {
	patterns := DefaultRawPatterns("goose")
	if patterns == nil {
		t.Fatal("DefaultRawPatterns(\"goose\") returned nil")
	}
	if len(patterns.BusyPatterns) == 0 {
		t.Error("goose busy patterns should not be empty")
	}
	if len(patterns.PromptPatterns) == 0 {
		t.Error("goose prompt patterns should not be empty")
	}
}
