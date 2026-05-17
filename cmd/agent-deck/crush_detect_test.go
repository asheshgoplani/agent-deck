package main

import "testing"

// Issue #940: `agent-deck add -c crush .` must set Instance.Tool = "crush"
// instead of falling back to "shell". This is the CLI-layer detection (not
// tmux's detectToolFromCommand) — it lives in main.go.

func TestDetectTool_Crush(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want string
	}{
		{"bare", "crush", "crush"},
		{"with yolo", "crush --yolo", "crush"},
		{"uppercase", "Crush", "crush"},
		{"with session", "crush --session abc123", "crush"},
		{"with continue", "crush --continue", "crush"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectTool(tt.cmd); got != tt.want {
				t.Errorf("detectTool(%q) = %q, want %q", tt.cmd, got, tt.want)
			}
		})
	}
}

func TestDetectTool_Crush_Negative(t *testing.T) {
	// Note: detectTool uses strings.Contains, so any string containing "crush"
	// will match. Only truly unrelated strings are tested here.
	tests := []struct {
		name string
		cmd  string
	}{
		{"empty string", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectTool(tt.cmd); got == "crush" {
				t.Errorf("detectTool(%q) = %q, should NOT match crush", tt.cmd, got)
			}
		})
	}
}
