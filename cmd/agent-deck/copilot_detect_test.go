package main

import "testing"

// Issue #556: `agent-deck add -c copilot .` must set Instance.Tool = "copilot"
// instead of falling back to "shell". This is the CLI-layer detection (not
// tmux's detectToolFromCommand) — it lives in main.go.

func TestDetectTool_Copilot(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want string
	}{
		{"bare", "copilot", "copilot"},
		{"with flags", "copilot --resume", "copilot"},
		{"uppercase", "Copilot", "copilot"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectTool(tt.cmd); got != tt.want {
				t.Errorf("detectTool(%q) = %q, want %q", tt.cmd, got, tt.want)
			}
		})
	}
}

// Cursor CLI (`cursor agent`) must register as tool "cursor", not "shell".
func TestDetectTool_Cursor(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want string
	}{
		{"agent subcommand", "cursor agent", "cursor"},
		{"bare binary", "cursor", "cursor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectTool(tt.cmd); got != tt.want {
				t.Errorf("detectTool(%q) = %q, want %q", tt.cmd, got, tt.want)
			}
		})
	}
}

// Pi must detect via token match (not strings.Contains) so short-name
// false matches like "epic", "tapioca", "spider", "happiness" don't
// hijack the tool identity. Co-credit @masta-g3 (PR #674).
func TestDetectTool_Pi(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want string
	}{
		{"bare", "pi", "pi"},
		{"with flags", "pi --profile dev", "pi"},
		{"uppercase", "Pi", "pi"},
		{"no match in epic", "epic", "shell"},
		{"no match in tapioca", "tapioca", "shell"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectTool(tt.cmd); got != tt.want {
				t.Errorf("detectTool(%q) = %q, want %q", tt.cmd, got, tt.want)
			}
		})
	}
}
