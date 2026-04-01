package ui

import "testing"

func TestCleanPaneTitle(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"generic claude", "✳ Claude Code", ""},
		{"generic gemini", "✳ Gemini CLI", ""},
		{"generic codex", "✳ Codex CLI", ""},
		{"braille spinner with task", "⠐ Fix the KPIs (Branch)", "Fix the KPIs (Branch)"},
		{"done marker with task", "✳ Run and verify session tests", "Run and verify session tests"},
		{"multiple markers", "✳✻ Some task", "Some task"},
		{"just markers", "✳✻✽", ""},
		{"no markers", "Hello world", "Hello world"},
		{"hostname only", "29fa91017da8", "29fa91017da8"},
		{"braille only", "⠐ Claude Code", ""},
		{"whitespace after strip", "✳  Spaced task ", "Spaced task"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanPaneTitle(tt.input)
			if got != tt.want {
				t.Errorf("cleanPaneTitle(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
