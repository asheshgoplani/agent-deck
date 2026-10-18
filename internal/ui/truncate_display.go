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

// truncateDisplayLeft is truncateDisplay from the left for plain text (a
// path, no escape sequences): it keeps the end of s and starts with head
// when it had to cut. A wide rune that does not fit whole is dropped.
func truncateDisplayLeft(s string, width int, head string) string {
	if width < 1 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	if width <= lipgloss.Width(head) {
		head = ""
	}
	keep := width - lipgloss.Width(head)
	rs := []rune(s)
	used, start := 0, len(rs)
	for start > 0 {
		w := lipgloss.Width(string(rs[start-1]))
		if used+w > keep {
			break
		}
		used += w
		start--
	}
	return head + string(rs[start:])
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
