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

// piComposerBoxVisible reports whether the frame ends with pi's editor box:
// a horizontal-rule border, 1..12 input lines, another rule, with the lower
// rule among the last 8 non-blank lines (pi's footer and widgets draw under
// it). It keys on the editor's shape, not on footer text, so it holds when an
// extension such as pi-powerline-footer replaces pi's footer and editor
// (#3098). Only the overdue startup probe uses it; normal waiting/active
// classification is unchanged.
func piComposerBoxVisible(content string) bool {
	lines := strings.Split(content, "\n")
	end := len(lines) - 1
	for end >= 0 && strings.TrimSpace(lines[end]) == "" {
		end--
	}
	nonBlank := 0
	for lower := end; lower > 0 && nonBlank < 8; lower-- {
		if strings.TrimSpace(lines[lower]) == "" {
			continue
		}
		nonBlank++
		if !isPiComposerBorder(lines[lower]) {
			continue
		}
		for upper := lower - 1; upper >= 0 && lower-upper-1 <= 12; upper-- {
			if isPiComposerBorder(lines[upper]) {
				if lower-upper-1 >= 1 {
					return true
				}
				break
			}
		}
	}
	return false
}

// isPiComposerBorder reports whether line is a border of pi's editor box: a
// plain rule (isPiComposerRule), or a rule carrying the editor's scroll
// marker, stock "─── ↑ 3 more ───" or powerline "↑─────".
func isPiComposerBorder(line string) bool {
	trimmed := strings.TrimSpace(line)
	if isPiComposerRule(trimmed) {
		return true
	}
	for _, arrow := range []string{"↑", "↓"} {
		before, after, ok := strings.Cut(trimmed, arrow)
		if !ok {
			continue
		}
		after = strings.TrimLeft(after, " ")
		if digits := strings.TrimLeft(after, "0123456789"); len(digits) < len(after) {
			rest, ok := strings.CutPrefix(digits, " more")
			if !ok {
				return false
			}
			after = rest
		}
		return isPiComposerRule(strings.TrimSpace(before) + strings.TrimSpace(after))
	}
	return false
}
