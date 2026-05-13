package main

import "testing"

// Hermes Agent CLI: `agent-deck add -c hermes .` must set Instance.Tool = "hermes"
// instead of falling back to "shell". This is the CLI-layer detection (not
// tmux's detectToolFromCommand) — it lives in main.go.

func TestDetectTool_Hermes(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want string
	}{
		{"bare", "hermes", "hermes"},
		{"with flags", "hermes --yolo", "hermes"},
		{"uppercase", "Hermes", "hermes"},
		{"with resume", "hermes --resume 20260225_143052_a1b2c3", "hermes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectTool(tt.cmd); got != tt.want {
				t.Errorf("detectTool(%q) = %q, want %q", tt.cmd, got, tt.want)
			}
		})
	}
}

func TestDetectTool_Hermes_Negative(t *testing.T) {
	// Note: detectTool uses strings.Contains, so "hermesctl" would match.
	// This is a known limitation shared with all tools (copilot, codex, etc.).
	// Only truly unrelated strings are tested here.
	tests := []struct {
		name string
		cmd  string
	}{
		{"empty string", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectTool(tt.cmd); got == "hermes" {
				t.Errorf("detectTool(%q) = %q, should NOT match hermes", tt.cmd, got)
			}
		})
	}
}
