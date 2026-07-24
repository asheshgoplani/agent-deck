// Package send consolidates prompt detection and send verification functions
// used by both the CLI send path (session_cmd.go) and the Instance send path
// (instance.go). Having a single source of truth prevents fix divergence.
package send

import (
	"strings"
)

// HasUnsentPastedPrompt detects Claude's composer marker for a pasted-but-unsent prompt.
// Example: "[Pasted text #1 +89 lines]".
func HasUnsentPastedPrompt(content string) bool {
	return strings.Contains(strings.ToLower(content), "[pasted text")
}

// firstNonEmptyLine returns the first physical line of s that is non-empty
// after trimming. Used to reconstruct what the composer actually renders for a
// multi-line message: Claude's input box shows the message's first physical
// line and truncates the rest behind a blank line (see HasUnsentComposerPrompt).
func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}

// messageTail returns the normalized content of message that follows its first
// non-empty physical line. For a multi-line message this "tail" is the part that
// makes it identifiable beyond an opening line another draft might share, and is
// used to corroborate a first-line match before recovering (see
// HasUnsentComposerPrompt).
func messageTail(message string) string {
	lines := strings.Split(message, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != "" {
			return NormalizePromptText(strings.Join(lines[i+1:], " "))
		}
	}
	return ""
}

// maxTailNeedleLen bounds how much of a multi-line message's tail must be
// visible in the pane to corroborate a first-line match: long enough to be
// message-specific, short enough to tolerate terminal wrapping of the content.
const maxTailNeedleLen = 48

// paneContainsTail reports whether a bounded, message-specific fragment of a
// multi-line message's tail is visible in the pane content. It corroborates a
// first-line match so an unrelated draft that merely shares the same opening
// line is not mistaken for this message.
func paneContainsTail(content, tail string) bool {
	if tail == "" {
		return false
	}
	needle := []rune(tail)
	if len(needle) > maxTailNeedleLen {
		needle = needle[:maxTailNeedleLen]
	}
	return strings.Contains(NormalizePromptText(content), string(needle))
}

// NormalizePromptText normalizes whitespace in prompt text by replacing NBSP
// with regular spaces, trimming, and collapsing multiple whitespace runs.
func NormalizePromptText(s string) string {
	s = strings.ReplaceAll(s, "\u00a0", " ")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return strings.Join(strings.Fields(s), " ")
}

// IsComposerDividerLine detects composer divider lines made of dash characters.
// Requires at least 10 consecutive dash-like characters (─, -, ━).
func IsComposerDividerLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	count := 0
	for _, r := range line {
		if r == '─' || r == '-' || r == '━' {
			count++
			continue
		}
		return false
	}
	return count >= 10
}

// ParsePromptFromComposerBlock parses the prompt text from a composer block
// (the lines between two divider lines). It looks for a prompt marker (❯ or ›)
// and collects any wrapped continuation lines.
func ParsePromptFromComposerBlock(lines []string) (string, bool) {
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], " \t\r")
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" {
			continue
		}

		markerLen := 0
		for _, marker := range []string{"❯", "›"} {
			if strings.HasPrefix(trimmed, marker) {
				markerLen = len(marker)
				break
			}
		}
		if markerLen == 0 {
			continue
		}

		bodyParts := []string{strings.TrimSpace(trimmed[markerLen:])}
		for j := i + 1; j < len(lines); j++ {
			cont := strings.TrimRight(lines[j], " \t\r")
			if strings.TrimSpace(cont) == "" {
				if len(bodyParts) > 0 && bodyParts[len(bodyParts)-1] != "" {
					break
				}
				continue
			}
			// Wrapped composer lines are typically indented continuation lines.
			if strings.HasPrefix(cont, "  ") || strings.HasPrefix(cont, "\t") {
				bodyParts = append(bodyParts, strings.TrimSpace(cont))
				continue
			}
			break
		}

		return NormalizePromptText(strings.Join(bodyParts, " ")), true
	}
	return "", false
}

// CurrentComposerPrompt extracts the current prompt text from the composer region
// at the bottom of the terminal pane. It searches for the last two divider lines
// and parses the prompt between them, with a fallback for layouts without dividers.
func CurrentComposerPrompt(content string) (string, bool) {
	lines := strings.Split(content, "\n")
	if len(lines) > 240 {
		lines = lines[len(lines)-240:]
	}

	// Primary path: parse the explicit composer region between the last two
	// divider lines nearest the bottom of the pane.
	lastDivider := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if IsComposerDividerLine(lines[i]) {
			lastDivider = i
			break
		}
	}
	if lastDivider > 0 {
		prevDivider := -1
		for i := lastDivider - 1; i >= 0; i-- {
			if IsComposerDividerLine(lines[i]) {
				prevDivider = i
				break
			}
		}
		if prevDivider >= 0 && prevDivider+1 < lastDivider {
			if body, ok := ParsePromptFromComposerBlock(lines[prevDivider+1 : lastDivider]); ok {
				return body, true
			}
		}
	}

	// Fallback for layouts without clear divider lines: look near the bottom
	// for a strict prompt marker at the start of the line.
	start := 0
	if len(lines) > 40 {
		start = len(lines) - 40
	}
	for i := len(lines) - 1; i >= start; i-- {
		trimmed := strings.TrimLeft(lines[i], " \t")
		if strings.TrimSpace(trimmed) == "" {
			continue
		}
		for _, marker := range []string{"❯", "›"} {
			if strings.HasPrefix(trimmed, marker) {
				return NormalizePromptText(strings.TrimSpace(trimmed[len(marker):])), true
			}
		}
	}
	return "", false
}

// HasCurrentComposerPrompt returns true if a composer prompt is visible in the
// terminal pane content.
func HasCurrentComposerPrompt(content string) bool {
	_, ok := CurrentComposerPrompt(content)
	return ok
}

// HasUnsentComposerPrompt detects when the message text is still present in the
// interactive input line (e.g., "❯ message"), which indicates Enter was not
// accepted yet even if no "[Pasted text ...]" marker is shown.
func HasUnsentComposerPrompt(content, message string) bool {
	msg := NormalizePromptText(message)
	if msg == "" {
		return false
	}

	promptBody, hasPrompt := CurrentComposerPrompt(content)
	if !hasPrompt {
		return false
	}
	promptBody = NormalizePromptText(promptBody)
	if promptBody == "" {
		return false
	}

	// Direct match (short prompts or fully visible single-line prompts).
	if strings.HasPrefix(promptBody, msg) || strings.Contains(promptBody, msg) {
		return true
	}

	// Wrapped prompts: Claude often shows only the first visual line of the
	// current composer input (message wraps to following indented lines).
	// If the visible prompt line is a substantial prefix of the message,
	// Enter was not accepted yet.
	const minWrappedPrefixLen = 16
	if len(promptBody) >= minWrappedPrefixLen && strings.HasPrefix(msg, promptBody) {
		return true
	}

	// Multi-line messages: CurrentComposerPrompt stops collecting at the first
	// blank line, so the composer body it recovers is only the message's first
	// physical line. This is the shape of every `launch -m` / `session send`
	// prompt once the completion-sentinel instruction is appended — its trailing
	// "\n\n## Final step …" block introduces a blank line. On a cold first start
	// the initial Enter can be swallowed while the child's TUI is still mounting,
	// leaving the whole message sitting unsent in the composer; when the first
	// line is shorter than minWrappedPrefixLen the whole-message comparisons
	// above all miss it and the send-verify loop never fires a recovery Enter.
	//
	// Match the message's first physical line so that stuck state is detected
	// regardless of first-line length — but a first-line match alone is
	// ambiguous: two different messages can open with the same line, so acting
	// on it risks firing a recovery Enter that submits an unrelated draft. Guard
	// it with corroborating message-specific content: a fragment of the tail
	// (everything past the first line) must also be visible in the pane. Only
	// engages for genuinely multi-line messages (firstLine != msg), so
	// single-line behavior is unchanged.
	if firstLine := NormalizePromptText(firstNonEmptyLine(message)); firstLine != "" && firstLine != msg {
		firstLineMatch := promptBody == firstLine ||
			(len(promptBody) >= minWrappedPrefixLen && strings.HasPrefix(firstLine, promptBody))
		if firstLineMatch && paneContainsTail(content, messageTail(message)) {
			return true
		}
	}

	// Fallback: compare a short message prefix to handle truncation/formatting
	// differences while avoiding over-broad matching.
	needle := msg
	if len(needle) > 32 {
		needle = needle[:32]
	}
	if strings.Contains(promptBody, needle) {
		return true
	}

	return false
}
