package tmux

import (
	"testing"
)

func TestCleanPaneTitle(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		// Empty string stays empty
		{"empty string", "", ""},

		// Generic tool titles collapse to empty
		{"Claude Code title", "Claude Code", ""},
		{"Gemini CLI title", "Gemini CLI", ""},
		{"Codex CLI title", "Codex CLI", ""},

		// Braille spinner prefix is stripped, leaving the description
		{"braille spinner ⠋ + description", "⠋ Refactoring auth", "Refactoring auth"},
		{"braille spinner ⠙ + description", "⠙ Building tests", "Building tests"},
		{"braille spinner multiple + text", "⠋⠙ Running migrations", "Running migrations"},

		// Done-marker prefix (StripSpinnerRunes strips ✳ ✽ ✶ ✻ ✢ ·)
		{"done marker ✳ + description", "✳ Worked for 54s", "Worked for 54s"},
		{"done marker ✻ + description", "✻ Task complete", "Task complete"},
		{"done marker ✽ + description", "✽ Finished", "Finished"},
		{"done marker ✶ + description", "✶ Done patching", "Done patching"},
		{"done marker ✢ + description", "✢ Committed changes", "Committed changes"},
		{"middle-dot marker · + description", "· Something done", "Something done"},

		// Braille char outside SpinnerRuneSet (U+2800-28FF range): stripped by TrimLeftFunc
		{"U+2800 braille space prefix", string(rune(0x2800)) + " Some task", "Some task"},
		{"U+28FF high braille prefix", string(rune(0x28FF)) + " Other task", "Other task"},

		// Plain description with no markers → unchanged
		{"plain description", "Implement login flow", "Implement login flow"},
		{"plain with numbers", "Fix issue #42", "Fix issue #42"},

		// Leading/trailing whitespace trimmed
		{"leading whitespace", "   my task", "my task"},
		{"trailing whitespace", "my task   ", "my task"},
		{"both sides whitespace", "  do something  ", "do something"},

		// After stripping markers, if only whitespace remains → empty
		{"only whitespace", "   ", ""},

		// A title that becomes one of the generic names after stripping is untouched
		// (the generic names don't have markers, so they hit the switch directly)
		{"generic title with trailing space", "Claude Code ", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CleanPaneTitle(tt.input)
			if got != tt.want {
				t.Errorf("CleanPaneTitle(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
