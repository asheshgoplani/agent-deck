// Package send consolidates prompt detection and send verification functions
// used by both the CLI send path (session_cmd.go) and the Instance send path
// (instance.go). Having a single source of truth prevents fix divergence.
package send

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// HasUnsentPastedPrompt detects Claude's composer marker for a pasted-but-unsent prompt.
// Example: "[Pasted text #1 +89 lines]".
func HasUnsentPastedPrompt(content string) bool {
	return CountPasteMarkers(content) > 0
}

// CountPasteMarkers reports HOW MANY "[Pasted text …]" markers content holds.
// It is the same match HasUnsentPastedPrompt makes, kept as one implementation
// so the boolean and the count can never drift apart.
//
// The count exists because presence alone is not a signal for a caller that
// must attribute a marker to ONE send: a submitted paste leaves its collapsed
// marker on screen for good, so "a marker is visible" is permanently true
// after the first multi-line delivery to a pane, while "one more marker than
// before" stays true of each subsequent send (issue #1855). Callers measuring
// a delta must count; callers asking a yes/no question about the pane right
// now (the #1777 attribution gate) must not.
func CountPasteMarkers(content string) int {
	return strings.Count(strings.ToLower(content), "[pasted text")
}

// pasteMarkerLineCountRE matches Claude's (and codex's) "[Pasted text #N +M
// lines]" collapse marker and captures M — the count the composer declares
// for that paste. Case-insensitive to match CountPasteMarkers' lower-cased
// scan. "\s*" around the count lets a marker that the pane soft-wrapped
// across two rows ("+5" / "lines]") still parse.
var pasteMarkerLineCountRE = regexp.MustCompile(`(?i)\[pasted text[^\]]*\+\s*(\d+)\s*lines?\]`)

// PasteMarkerLineCounts returns the M declared by every "[Pasted text #N +M
// lines]" marker in content, in order of appearance. Empty when no marker is
// present, or a marker is present but its count could not be parsed. A bare
// "[Pasted text #N]" (Claude's marker for a long paste with no line break)
// carries no count and is not reported.
//
// This is issue #2079's detection primitive: the framed-paste transport
// (tmux paste-buffer -p -r, see internal/tmux) is the only delivery path for
// a multi-line prompt, and Claude's composer collapses whatever landed behind
// this marker whether the paste arrived whole or was cut short by a
// remounting composer swallowing the tail end of the write. A truncated paste
// still produces a well-formed marker — just one declaring fewer line breaks
// than the message actually has — so the declared count is the one signal
// that distinguishes a genuine delivery from a partial one without needing
// to read back the (now collapsed and unreadable) literal text.
//
// M is NOT a line count and NOT a display-row count. Claude Code prints the
// number of hard line-break sequences in the pasted text (its formatter is
// literally `(text.match(/\r\n|\r|\n/g) || []).length`, so a 6-line prompt is
// "+5 lines"), and the pane width plays no part in it. Compare it only with
// ExpectedPasteMarkerLineBreaks, never with a physical line count: the rc.3
// launch regression was exactly that off-by-one, refusing every multi-line
// launch prompt as "truncated".
func PasteMarkerLineCounts(content string) []int {
	matches := pasteMarkerLineCountRE.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	counts := make([]int, 0, len(matches))
	for _, m := range matches {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		counts = append(counts, n)
	}
	return counts
}

// ExpectedPasteMarkerLineBreaks returns the M Claude's composer declares in
// its "[Pasted text #N +M lines]" marker when message is delivered through
// the framed-paste transport: the number of HARD line breaks in the text,
// after CRLF and bare-CR breaks are normalized to LF exactly as the tmux
// transport normalizes them before pasting (see sendKeysChunkedToTarget).
// Soft wrapping in the pane is not a line break and never enters the count.
//
// This counts every hard break in the normalized text, including a trailing
// one, matching Claude's own documented formula
// `(text.match(/\r\n|\r|\n/g) || []).length` literally rather than assuming
// it (or some upstream paste handler) trims a trailing break before
// counting. An earlier version excluded trailing breaks as a defensive
// "floor" against that unverified assumption, but a floor is ambiguous by
// construction: a message ending in "\n" and a paste truncated to exactly
// one line short of it can both land on the same floored expectation, so a
// lost last line goes undetected (rc.4 P2-1). Counting literally removes
// that ambiguity — a truncation that drops the message's own trailing break
// is now indistinguishable from any other dropped line, and is caught.
//
// Returns 0 for a message with no hard line break: such a paste, if long
// enough to collapse at all, renders as a bare "[Pasted text #N]" with no
// count to compare, and a short one goes out as `send-keys -l` and never
// collapses. Callers skip the check at 0.
func ExpectedPasteMarkerLineBreaks(message string) int {
	normalized := message
	if strings.Contains(normalized, "\r") {
		normalized = strings.ReplaceAll(normalized, "\r\n", "\n")
		normalized = strings.ReplaceAll(normalized, "\r", "\n")
	}
	return strings.Count(normalized, "\n")
}

// PasteMarkerVerdict is CheckPasteMarker's classification of the paste
// marker(s) visible in a pane against the message that was pasted.
type PasteMarkerVerdict int

const (
	// PasteMarkerAbsent: no counted marker is rendered (yet). Either the
	// composer has not repainted, or this pane never frames pastes. Callers
	// treat it as "unknown, not unsafe" and fall back to pre-#2079 behavior.
	PasteMarkerAbsent PasteMarkerVerdict = iota
	// PasteMarkerIntact: the newest marker declares at least as many hard
	// line breaks as the message has — every line arrived.
	PasteMarkerIntact
	// PasteMarkerTruncated: the newest marker declares fewer hard line
	// breaks than the message has — a fragment, not the whole prompt, is in
	// the composer. Enter must be withheld / the delivery must be reported.
	PasteMarkerTruncated
)

// String makes a verdict readable in log lines and test failures.
func (v PasteMarkerVerdict) String() string {
	switch v {
	case PasteMarkerAbsent:
		return "absent"
	case PasteMarkerIntact:
		return "intact"
	case PasteMarkerTruncated:
		return "truncated"
	}
	return fmt.Sprintf("PasteMarkerVerdict(%d)", int(v))
}

// CheckPasteMarker is the single #2079 discriminator shared by every send
// site (sendMessageWhenReady's pre-Enter hook and launch --no-wait's
// post-submit poller): it reads the newest "[Pasted text #N +M lines]" marker
// in content and compares M against expectedBreaks (from
// ExpectedPasteMarkerLineBreaks). The returned declared value is that M, or
// -1 when no counted marker is visible.
//
// The newest marker wins because a transcript keeps every earlier paste's
// collapsed marker on screen for good (#1855); only the most recent one can
// describe this send.
func CheckPasteMarker(content string, expectedBreaks int) (verdict PasteMarkerVerdict, declared int) {
	counts := PasteMarkerLineCounts(content)
	if len(counts) == 0 {
		return PasteMarkerAbsent, -1
	}
	declared = counts[len(counts)-1]
	if declared < expectedBreaks {
		return PasteMarkerTruncated, declared
	}
	return PasteMarkerIntact, declared
}

// PasteTruncationCheck builds the pre-Enter guard both send paths use
// (Instance.sendMessageWhenReady on the launch path, sendInitialKeysChecked
// behind `session send`): it reads the composer's newest paste marker and
// reports ok=false — withholding Enter — only when the marker proves a
// fragment, not the whole prompt, landed (issue #2079).
//
// The returned function has tmux.PostPasteCheck's shape and is assignable to
// it; the type is spelled structurally here so this package keeps no
// dependency on internal/tmux.
//
// Three outcomes, only one of which refuses:
//   - capture failed: unknown, not unsafe. Proceed, exactly as the pre-#2079
//     bare Enter did, rather than blocking delivery on a pane-read glitch.
//   - no marker, or an intact one: either the composer has not repainted yet
//     (benign render lag, which the callers' verify loops still catch) or the
//     pane never frames pastes at all. Proceed.
//   - truncated: refuse, with an error naming the declared and expected
//     break counts.
func PasteTruncationCheck(expectedBreaks int) func(pane string, captureErr error) (bool, error) {
	return func(pane string, captureErr error) (bool, error) {
		if captureErr != nil {
			return true, nil
		}
		verdict, declared := CheckPasteMarker(pane, expectedBreaks)
		if verdict == PasteMarkerTruncated {
			return false, fmt.Errorf(
				"prompt truncated in transit: composer shows a paste with %d line breaks ([Pasted text +%d lines]) but the message has %d; refusing to submit a partial prompt",
				declared, declared, expectedBreaks)
		}
		return true, nil
	}
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
