package ui

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// minTruncatedText is the narrowest room in which a shortened text still
// says something: one character and a three-column tail.
const minTruncatedText = 4

// truncateDisplay shortens s to at most width display columns, ending in
// tail when it had to cut. It counts display columns, never splits a rune or
// an escape sequence, and returns "" for a width below 1. When the tail
// alone would fill the room, s is cut without it.
func truncateDisplay(s string, width int, tail string) string {
	if width < 1 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	if width <= lipgloss.Width(tail) {
		tail = ""
	}
	return ansi.Truncate(s, width, tail)
}

// fitEmptyStateText fits a subtitle or hint into room columns: unchanged
// when it fits, shortened with "..." when there is room for that, and left
// out (ok false) when there is not.
func fitEmptyStateText(s string, room int) (string, bool) {
	if lipgloss.Width(s) <= room {
		return s, true
	}
	if room < minTruncatedText {
		return "", false
	}
	return truncateDisplay(s, room, "..."), true
}
