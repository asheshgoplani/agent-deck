package tmux

import "strings"

// isHorizontalSeparator returns true when a line is a horizontal separator made
// of box-drawing dash characters (U+2500), at least 8 runes wide.
// ASCII hyphens are intentionally excluded because they appear in prose/markdown.
func isHorizontalSeparator(line string) bool {
	clean := strings.TrimSpace(StripANSI(line))
	runes := []rune(clean)
	if len(runes) < 8 {
		return false
	}
	for _, r := range runes {
		if r != '\u2500' {
			return false
		}
	}
	return true
}

// linesAfterLastSeparator finds the last horizontal separator in content and
// returns all lines below it. If the separator is the final non-blank line
// (idle layout), returns lines between the last two separators instead.
// Falls back to lastNLines(content, 3) when no separator exists.
func linesAfterLastSeparator(content string) []string {
	lines := strings.Split(content, "\n")
	// Trim trailing blank lines.
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return nil
	}

	// Find the last separator index.
	lastSep := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if isHorizontalSeparator(lines[i]) {
			lastSep = i
			break
		}
	}

	if lastSep < 0 {
		// No separator found; fall back to fixed line count.
		return lastNLines(content, 3)
	}

	// If the separator is the last non-blank line, the status bar is between
	// the second-to-last separator and this one (idle layout with trailing sep).
	after := lines[lastSep+1:]
	allBlank := true
	for _, l := range after {
		if strings.TrimSpace(l) != "" {
			allBlank = false
			break
		}
	}
	if allBlank || len(after) == 0 {
		prevSep := -1
		for i := lastSep - 1; i >= 0; i-- {
			if isHorizontalSeparator(lines[i]) {
				prevSep = i
				break
			}
		}
		if prevSep >= 0 {
			return lines[prevSep+1 : lastSep]
		}
		return lastNLines(content, 3)
	}

	return after
}
