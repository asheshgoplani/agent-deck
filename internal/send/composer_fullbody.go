package send

import "strings"

// ComposerFullBodyReader returns the WHOLE text of the visible composer from
// a raw (ANSI-bearing) pane capture, every row including blank ones, and ok =
// false when the frame has no composer it can read completely. Unlike a
// ComposerDraftReader (which may stop at the first paragraph), it sees every
// row, so ObserveComposer can say whether the whole input box is this send's
// text. It is only ever read; nothing is deleted through it.
type ComposerFullBodyReader func(raw string, strip func(string) string) (body string, ok bool)

// ClaudeComposerFullBody is Claude Code's ComposerFullBodyReader: every row
// between ComposerBounds' top and bottom borders, blank rows included, with
// the leading prompt glyph removed from the first row and the two-column
// continuation indent from the rest; trailing blank rows are dropped. A dim
// prompt suggestion (ComposerBodyIsSuggestion, decided on the raw SGR) reads
// as an empty body. Plain text shaped like Claude's idle placeholder or
// queued hint is returned as it is: only the caller knows whether it is a
// prefix of its own message (ObserveComposer) or may be read as empty
// (FullBodyEmpty). ok is
// false when the frame has no bounded composer, or its first non-blank row
// does not start with the prompt glyph.
//
// Row joins are reported as "\n" whether they are hard line breaks or soft
// wraps; compare bodies through NormalizePromptText.
func ClaudeComposerFullBody(raw string, strip func(string) string) (string, bool) {
	strip = orIdentity(strip)
	if ComposerBodyIsSuggestion(raw) {
		return "", true
	}
	lines := composerSearchLines(strip(raw))
	top, bottom := ComposerBounds(lines)
	if top < 0 || top+1 >= bottom {
		return "", false
	}
	rows := lines[top+1 : bottom]
	first := -1
	for i, row := range rows {
		if strings.TrimSpace(row) != "" {
			first = i
			break
		}
	}
	if first < 0 {
		return "", false
	}
	lead := strings.TrimLeft(strings.TrimRight(rows[first], " \t\r"), " \t")
	if !strings.HasPrefix(lead, "❯") {
		return "", false
	}
	body := []string{strings.TrimPrefix(strings.TrimPrefix(lead, "❯"), " ")}
	for _, row := range rows[first+1:] {
		row = strings.TrimRight(row, " \t\r")
		for range 2 {
			row = strings.TrimPrefix(row, " ")
		}
		body = append(body, row)
	}
	for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
		body = body[:len(body)-1]
	}
	return strings.Join(body, "\n"), true
}

// FullBodyEmpty reports whether a ComposerFullBodyReader body is an empty
// composer for a caller that has no message of its own in it (the pre-send
// guard and probe): empty, or shaped like Claude's idle placeholder or queued
// hint. A caller that typed a message must decide ownership first
// (ObserveComposer).
func FullBodyEmpty(body string) bool {
	d := NormalizePromptText(body)
	return d == "" || IsComposerPlaceholder(d)
}

// IsPasteChip reports whether a normalized composer body is exactly one
// collapsed paste marker ("[Pasted text #1 +3 lines]"). The label is
// generic, not unique to any one message.
func IsPasteChip(body string) bool {
	return pasteChipRe.MatchString(NormalizePromptText(body))
}
