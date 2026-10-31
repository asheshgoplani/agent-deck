package tmux

import "strings"

// piBusyPatternContent blanks complete fenced examples while preserving line
// positions in the original 25-line tail window. An unmatched fence is left intact:
// a streaming answer must not hide the live interrupt bar beneath it.
func piBusyPatternContent(content string) string {
	if !strings.Contains(content, "```") && !strings.Contains(content, "~~~") {
		return strings.Join(lastNLines(content, 25), "\n")
	}
	lines := lastNLines(content, strings.Count(content, "\n")+1)
	start, fence := -1, ""
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if start >= 0 {
			if strings.TrimRight(trimmed, fence[:1]) == "" && len(trimmed) >= len(fence) {
				for j := start; j <= i; j++ {
					lines[j] = ""
				}
				start, fence = -1, ""
			}
			continue
		}
		if !strings.HasPrefix(trimmed, "```") && !strings.HasPrefix(trimmed, "~~~") {
			continue
		}
		n := 0
		for n < len(trimmed) && trimmed[n] == trimmed[0] {
			n++
		}
		if trimmed[0] == '`' && strings.Contains(trimmed[n:], "`") {
			continue
		}
		start, fence = i, trimmed[:n]
	}
	return strings.Join(lines[max(0, len(lines)-25):], "\n")
}

// piLiveSpinnerContent blanks pi "── ⠴ Working ──" banners that are no longer
// at pi's live banner position, so the spinner scan reads only the live one
// (issue #2948).
//
// pi draws the live banner as the top border of its composer box: the banner,
// the draft lines, then the composer's bottom rule, with only widgets and the
// footer under it. When a turn ends the banner becomes a plain rule again. A
// banner with a complete composer box (two plain rule lines) still below it
// is therefore residue the redraw missed, not a running turn. A frame with no
// composer box below the banner keeps the old whole-frame reading, and a
// spinner that is not in the banner form (legacy "⠴ Working" status line) is
// left alone.
func piLiveSpinnerContent(content string, spinnerChars []string) string {
	lines := strings.Split(content, "\n")
	rulesBelow := 0
	changed := false
	for i := len(lines) - 1; i >= 0; i-- {
		if isPiComposerRule(lines[i]) {
			rulesBelow++
			continue
		}
		if rulesBelow >= 2 && isPiWorkingBanner(lines[i], spinnerChars) {
			lines[i] = ""
			changed = true
		}
	}
	if !changed {
		return content
	}
	return strings.Join(lines, "\n")
}

// isPiComposerRule reports whether line is a plain horizontal rule, the shape
// of pi's composer borders when no banner sits on them.
func isPiComposerRule(line string) bool {
	trimmed := strings.TrimSpace(line)
	n := 0
	for _, r := range trimmed {
		if r != '─' && r != '━' && r != '═' {
			return false
		}
		n++
	}
	return n >= 8
}

// isPiWorkingBanner reports whether line is pi's banner form: a short
// horizontal rule followed by a spinner glyph ("── ⠴ Working ──").
func isPiWorkingBanner(line string, spinnerChars []string) bool {
	for _, ch := range spinnerChars {
		idx := strings.Index(line, ch)
		if idx < 0 {
			continue
		}
		if isHorizontalRuleBannerPrefix(strings.TrimSpace(line[:idx])) {
			return true
		}
	}
	return false
}
