//go:build !windows
// +build !windows

package tmux

import "testing"

// TestParseAlternateOn verifies the tmux #{alternate_on} flag is parsed as
// "1" => alternate screen active, everything else => normal screen. Whitespace
// (tmux appends a trailing newline) is tolerated; only an exact "1" is truthy so
// values like "10" never misread as active.
func TestParseAlternateOn(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{"one means alt-screen", "1", true},
		{"trailing newline tolerated", "1\n", true},
		{"surrounding whitespace tolerated", "  1  ", true},
		{"zero means normal screen", "0", false},
		{"zero with newline", "0\n", false},
		{"empty means normal screen", "", false},
		{"ten is not one", "10", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseAlternateOn(tt.output); got != tt.want {
				t.Fatalf("parseAlternateOn(%q) = %v, want %v", tt.output, got, tt.want)
			}
		})
	}
}
