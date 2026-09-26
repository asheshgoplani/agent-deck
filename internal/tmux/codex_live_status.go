package tmux

import (
	"regexp"
	"strings"
)

// Codex's live row has an optional spinner (solid, hollow or hidden), a
// capitalised label and an elapsed-time interrupt hint. Narrow panes can
// truncate the closing parenthesis after the hint, or cut the row just
// after it (")…", ") …", ") ·…").
var codexStatusLineRe = regexp.MustCompile(`^(?:[•◦]\s+)?\p{Lu}.*\((?:\d+[hms]\s*)+[•·]\s*(?:esc|ctrl ?\+ ?c) to interrupt(?:\)(?: · .*| ?·?…)?|…)$`)

// codexLiveStatusLine reports whether a Codex frame shows the live status
// line in its live slot immediately before the "› " composer, with only
// blank lines and status details between the two.
//
// Codex draws that line four to six rows above the bottom (composer and
// model/context footer below it), so the plain "esc to interrupt" busy
// strings, gated to the last 3 lines, never see it. It also renders every
// agent message and "• Ran <cmd>" header at column 0 with "• ", so an idle
// pane whose last answer or command quotes the status shape must not match:
// those blocks are followed by their own continuation lines or a
// "─ Worked for … ─" rule before the composer.
//
// While a turn runs, Codex 0.155 lists the operator's queued messages between
// the status line and the composer ("• Queued follow-up inputs", one
// "  ↳ <message>" row each with indented continuations, then "    shift + ←
// edit last queued message"). That block is skipped whole, and only when it
// ends at its own header, so the status line above it still counts.
func codexLiveStatusLine(content string) bool {
	lines := lastNLines(content, 60)
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
		if details := codexStatusDetailsStart(lines, i); details >= 0 {
			i = details
			continue
		}
		if header := codexQueuedInputsHeader(lines, i); header >= 0 {
			i = header
			continue
		}
		return codexStatusLineRe.MatchString(strings.TrimRight(line, " \t"))
	}
	return false
}

// codexStatusDetailsStart finds the "  └ " row above wrapped detail rows.
// Codex uses four spaces for every continuation of a status detail.
func codexStatusDetailsStart(lines []string, end int) int {
	for i := end; i >= 0; i-- {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "  └ "):
			if i < end {
				return i
			}
			return -1
		case strings.HasPrefix(line, "    ") && strings.TrimSpace(line) != "":
		case strings.TrimSpace(line) == "" && i < end:
			// An empty detail line renders as an indent-only row.
		default:
			return -1
		}
	}
	return -1
}

const codexQueuedInputsTitle = "• Queued follow-up inputs"

// codexQueuedInputsHeader returns the index of the "• Queued follow-up inputs"
// header when lines[end] is the last row of that block, or -1. Every row
// between the header and end must be a "  ↳ " entry or an indented
// continuation, and at least one entry must exist.
func codexQueuedInputsHeader(lines []string, end int) int {
	entries := 0
	for i := end; i >= 0; i-- {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "  ↳ "):
			entries++
		case strings.HasPrefix(line, "    ") && strings.TrimSpace(line) != "":
			// Continuation row of an entry, or the "shift + ←" hint.
		case strings.TrimRight(line, " ") == codexQueuedInputsTitle && entries > 0 && i < end:
			return i
		default:
			return -1
		}
	}
	return -1
}
