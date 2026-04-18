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
