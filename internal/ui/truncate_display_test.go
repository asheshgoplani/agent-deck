package ui

import (
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

func TestTruncateDisplay(t *testing.T) {
	for _, tc := range []struct {
		in, tail string
		width    int
		want     string
	}{
		{"hello world", "...", 8, "hello..."},
		{"hello", "...", 5, "hello"},
		{"hello", "...", 3, "hel"},
		{"hello", "...", 0, ""},
		{"hello", "...", -4, ""},
		{"Host — 12", "...", 7, "Host..."},
		{"ünïcödé", "…", 4, "ünï…"},
		{"⚡⚡⚡⚡", "...", 5, "⚡..."},
	} {
		if got := truncateDisplay(tc.in, tc.width, tc.tail); got != tc.want || !utf8.ValidString(got) || lipgloss.Width(got) > max(tc.width, 0) {
			t.Errorf("truncateDisplay(%q, %d, %q) = %q, want %q", tc.in, tc.width, tc.tail, got, tc.want)
		}
	}
}
