package tmux

import (
	"regexp"
	"strings"
)

// codexStatusLineRe is the shape of Codex's live status line: a column-0
// bullet, a label and the "(<elapsed> • esc to interrupt)" group
// ("• Working (9m 41s • esc to interrupt)", "• Waiting for background
// terminal (1h 06m 33s • esc to interrupt) · 1 background terminal running").
var codexStatusLineRe = regexp.MustCompile(`^•\s+\S.*\((?:\d+[hms]\s*)+•\s*esc to interrupt\)`)

// codexLiveStatusLine reports whether a Codex frame shows the live status
// line in its live slot: the last "•" block before the "› " composer, with
// only blank lines and "  └ …" command lines between the two.
//
// Codex draws that line four to six rows above the bottom (composer and
// model/context footer below it), so the plain "esc to interrupt" busy
// strings, gated to the last 3 lines, never see it. It also renders every
// agent message and "• Ran <cmd>" header at column 0 with "• ", so an idle
// pane whose last answer or command quotes the status shape must not match:
// those blocks are followed by their own continuation lines or a
// "─ Worked for … ─" rule before the composer.
func codexLiveStatusLine(content string) bool {
	lines := lastNLines(content, 25)
	composer := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "› ") {
			composer = i
			break
		}
	}
	for i := composer - 1; i >= 0; i-- {
		line := lines[i]
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "  └ ") {
			continue
		}
		return codexStatusLineRe.MatchString(line)
	}
	return false
}
