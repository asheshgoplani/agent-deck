package main

import "testing"

// `agent-deck add -c grok .` must set Instance.Tool = "grok" instead of
// falling back to "shell". This is the CLI-layer detection (not tmux's
// detectToolFromCommand) — it lives in main.go.

func TestDetectTool_Grok(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want string
	}{
		{"bare", "grok", "grok"},
		{"with model", "grok -m grok-build", "grok"},
		{"uppercase", "Grok", "grok"},
		{"always-approve", "grok --always-approve", "grok"},
		{"absolute path", "/Users/me/.grok/bin/grok", "grok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectTool(tt.cmd); got != tt.want {
				t.Errorf("detectTool(%q) = %q, want %q", tt.cmd, got, tt.want)
			}
		})
	}
}

func TestDetectTool_Grok_Negative(t *testing.T) {
	// detectTool uses strings.Contains, so only truly unrelated strings are
	// guarded here (mirrors the crush/hermes precedent).
	tests := []struct {
		name string
		cmd  string
	}{
		{"empty string", ""},
		{"unrelated", "ls -la"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectTool(tt.cmd); got == "grok" {
				t.Errorf("detectTool(%q) = %q, should NOT match grok", tt.cmd, got)
			}
		})
	}
}
