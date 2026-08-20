package tmux

import (
	"regexp"
	"strings"
)

// Claude renders two kinds of modal selection on top of the composer: a tool
// permission dialog ("Do you want to proceed? / ❯ 1. Yes / 2. No") and an
// AskUserQuestion decision menu ("❯ 1. Reclaim now … / 2. Fix the defect …").
// Both occupy the composer region, and BOTH are UI, not typed text.
//
// That distinction is the whole point of this file. The composer-draft guard
// (internal/send) parses the composer region, and to it a rendered menu is
// indistinguishable from an operator's half-typed prompt: it saved the option
// list as a "draft", pressed Ctrl+C — which DISMISSES the question — sent its
// own message, then typed the menu's rendering back into the composer as
// literal text.
//
// Observed 2026-08-20 on an orchestrate conductor: its 15-minute heartbeat ate
// two AskUserQuestion decision prompts in 45 minutes. Each time the conductor
// re-asked, the next beat destroyed the question again, and the run sat for
// over an hour with three finished children behind a decision the human was
// never given the chance to answer. Nothing in the pane text distinguished
// that from a healthy idle prompt, so every status/substate observer called it
// idle-at-empty-prompt and no supervisor escalated.

// choiceFooterMarkers are footers Claude renders only while a modal selection
// is on screen. They are matched on the pane tail, so a phrase quoted earlier
// in the conversation cannot trip them.
var choiceFooterMarkers = []string{
	"Esc to cancel",
	"Use arrow keys to navigate",
	"Press Enter to select",
	"No, and tell Claude what to do differently",
	"Yes, allow once",
	"Yes, allow always",
	"Yes, and don't ask again",
	"Do you want to proceed?",
	"Do you trust the files in this folder?",
}

// selectedChoiceLine matches the SELECTED option of a menu: a selection marker
// and a numbered option on the SAME line ("❯ 1. Yes", "│ ❯ 1. Reclaim now").
// The marker is the discriminator that keeps ordinary prose out: an assistant
// message listing "1. The disk." above an empty "❯ " composer puts the marker
// on its own line and does not match.
var selectedChoiceLine = regexp.MustCompile(`^[❯›>]\s*([1-9][0-9]?)\.\s+\S`)

// otherChoiceLine matches any numbered option line, selected or not.
var otherChoiceLine = regexp.MustCompile(`^(?:[❯›>]\s*)?([1-9][0-9]?)\.\s+\S`)

// choiceTailLines is how far up the pane the scan reaches. A modal selection is
// always at the bottom; scanning further would start matching scrollback.
const choiceTailLines = 40

// PaneAwaitsChoice reports whether the pane is showing a modal selection that
// is waiting for a human to pick an option — a permission dialog or an
// AskUserQuestion menu.
//
// content must be ANSI-stripped pane text.
//
// The verdict is deliberately conservative in the direction that matters: a
// false positive only makes an automated sender refuse and escalate to a human
// (recoverable), while a false negative destroys the question (not).
func PaneAwaitsChoice(content string) bool {
	tail := paneTail(content, choiceTailLines)

	for _, marker := range choiceFooterMarkers {
		if strings.Contains(tail, marker) {
			return true
		}
	}

	// Structural fallback for menus whose footer this version does not render:
	// a selected numbered option plus at least one other option with a
	// DIFFERENT number. One number alone is a list item; two are a choice.
	selected := ""
	numbers := map[string]bool{}
	for _, line := range strings.Split(tail, "\n") {
		line = trimChoiceLine(line)
		if line == "" {
			continue
		}
		if m := selectedChoiceLine.FindStringSubmatch(line); m != nil {
			selected = m[1]
			numbers[m[1]] = true
			continue
		}
		if m := otherChoiceLine.FindStringSubmatch(line); m != nil {
			numbers[m[1]] = true
		}
	}
	return selected != "" && len(numbers) >= 2
}

// trimChoiceLine strips the leading whitespace and box-drawing gutter Claude
// draws around a dialog, so "│ ❯ 1. Yes" reduces to "❯ 1. Yes".
func trimChoiceLine(line string) string {
	line = strings.TrimSpace(line)
	for {
		r := []rune(line)
		if len(r) == 0 {
			return ""
		}
		switch r[0] {
		case '│', '┃', '║', '|':
			line = strings.TrimSpace(string(r[1:]))
			continue
		}
		return line
	}
}

// paneTail returns the last n lines of content.
func paneTail(content string, n int) string {
	lines := strings.Split(content, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
