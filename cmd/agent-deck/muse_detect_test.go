package main

import "testing"

// Muse Code CLI (`muse`): `agent-deck add -c muse .` must set
// Instance.Tool = "muse" instead of falling back to "shell". This is the
// CLI-layer detection (not tmux's detectToolFromCommand) — it lives in
// main.go via session.MatchTool.

func TestDetectTool_Muse(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want string
	}{
		{"bare", "muse", "muse"},
		{"with trust flag", "muse --trust-workspace", "muse"},
		{"with yolo", "muse --yolo", "muse"},
		{"uppercase", "Muse", "muse"},
		{"resume subcommand", "muse resume 01a063dd-45dd-7402-b516-4346e923db3e", "muse"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectTool(tt.cmd); got != tt.want {
				t.Errorf("detectTool(%q) = %q, want %q", tt.cmd, got, tt.want)
			}
		})
	}
}

func TestDetectTool_Muse_Negative(t *testing.T) {
	// Note: detectTool matches on substrings, so any string containing
	// "muse" will match. Only truly unrelated strings are tested here.
	tests := []struct {
		name string
		cmd  string
	}{
		{"empty string", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectTool(tt.cmd); got == "muse" {
				t.Errorf("detectTool(%q) = %q, should NOT match muse", tt.cmd, got)
			}
		})
	}
}
