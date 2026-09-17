// Package ctxtext holds the words the context inspector puts on a screen.
//
// It exists because one report has two renderers — the CLI in cmd/agent-deck,
// which is package main and which nothing can import, and the TUI pager in
// internal/ui — and they had already drifted on the screen that matters most.
// Asked about a session whose context window could not be established, the CLI
// printed a full sentence naming the reason and the fix, while the pager behind
// the C key printed the bare word "unknown". Same report, same field, two
// different answers, and the worse one was the one a user actually met.
//
// The rule this package encodes: an unknown is never a dead end. Whatever
// cannot be established is named, and the one line that would establish it is
// printed next to it, at the point of confusion rather than twenty-five lines
// below in a caveat block.
//
// Only the words live here. Wrapping, colour and layout stay with each surface,
// which is the part that legitimately differs between a fixed-width transcript
// and a resizable pane. Sentences are returned whole so each surface can wrap
// them at its own width — that is why nothing here takes a width.
package ctxtext

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
)

// WindowEnvVar is the override that supplies a window agent-deck cannot infer.
// It is declared here because it is the remedy every surface prints, and a
// second spelling of it somewhere else is a remedy that silently stops working.
const WindowEnvVar = "AGENTDECK_CONTEXT_WINDOW"

// WindowRemedy is the single action that turns an unknown window into a known
// one. It is deliberately short — it has to survive a header row on an 80-column
// terminal, and a remedy truncated to "set AGENTDECK_CONTEXT_WI…" is not one.
// The sentences below spell out what to set it to.
func WindowRemedy() string { return "set " + WindowEnvVar }

// WindowLine renders the window and where the figure came from, short enough
// for a header row.
//
// The unknown form carries the remedy rather than the reason: a header is the
// one place on either surface with no room for a sentence, and of the two the
// remedy is the half a reader can act on. The reason is not dropped — it is
// printed by [WindowGaugeSentences] beside the gauge, where the missing
// percentage is what prompts the question.
func WindowLine(w ctxinspect.WindowInfo) string {
	if !w.Known() {
		return "unknown — " + WindowRemedy()
	}
	out := TokenAmount(w.Tokens) + " tokens (" + w.Source.String()
	if w.Assumed() {
		out += ", assumed"
	}
	if d := strings.TrimSpace(w.Detail); d != "" {
		out += ": " + d
	}
	return out + ")"
}

// WindowUnknownReason states why there is no denominator. It never returns
// empty: "unknown" with no reason is the dead end this package exists to close.
func WindowUnknownReason(w ctxinspect.WindowInfo) string {
	if d := strings.TrimSpace(w.Detail); d != "" {
		return d
	}
	return "the context-window size could not be established, and a wrong denominator would misrepresent an otherwise honest total"
}

// WindowGaugeSentences explains an untrustworthy denominator directly beneath
// the gauge that used it — what is wrong, then how to fix it.
//
// Nothing here goes on the gauge line itself. That line is one line, it is the
// widest thing on the screen, and both surfaces truncate it; a remedy that gets
// cut to "set AGENTDECK_CONTEXT_WI…" is worse than no remedy at all. These
// sentences are returned whole so each surface wraps them at its own width,
// where they cannot be truncated and cannot be pushed twenty-five lines down
// into a caveat block nobody scrolls to.
//
// A known, established window produces nothing: silence is correct when there
// is nothing wrong with the figure.
func WindowGaugeSentences(w ctxinspect.WindowInfo) []string {
	switch {
	case !w.Known():
		return []string{
			"no percentage: " + WindowUnknownReason(w) + ".",
			"To get one, " + WindowRemedy() + " to this model's context-window size in tokens, then open this again.",
		}
	case w.Assumed():
		return []string{
			"≈ marks an assumed denominator: " + strings.TrimSuffix(strings.TrimSpace(w.Detail), ".") + ".",
			"The assumption is a lower bound on the real window, so the percentage errs high rather than low. " +
				"To replace it with the real figure, " + WindowRemedy() + ".",
		}
	default:
		return nil
	}
}

// PercentText renders a percentage, marked "≈" when its denominator was
// assumed rather than established.
func PercentText(w ctxinspect.WindowInfo, pct float64) string {
	mark := ""
	if w.Assumed() {
		mark = "≈"
	}
	return fmt.Sprintf("%s%.1f%%", mark, pct)
}

// IndeterminateBar is the occupancy widget with no scale to plot against.
//
// The bar keeps its shape when the window is unknown, because dropping it made
// the same screen unrecognisable to anyone who had seen it work on another
// session: they read "different, broken screen" where the truth is "same
// screen, one missing denominator". The fill is deliberately not the empty
// glyph a working gauge uses at 0% — an unknown that renders identically to
// zero is a lie told in punctuation.
func IndeterminateBar(width int) string {
	if width <= 0 {
		return ""
	}
	return "[" + strings.Repeat("·", width) + "]"
}

// TokenAmount renders a token count compactly. It lives here so the two
// renderers cannot disagree about what "1.0M" means.
func TokenAmount(n int) string {
	sign := ""
	if n < 0 {
		sign = "-"
		n = -n
	}
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%s%.1fM", sign, float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%s%.1fk", sign, float64(n)/1_000)
	default:
		return fmt.Sprintf("%s%d", sign, n)
	}
}

// ExactAmount renders a token figure to the digit, grouped in threes.
// TokenAmount rounds, which is what a dense column needs and the opposite of
// what a figure offered for verification needs: "27.0k" matches 26,951 and
// 27,013 equally well, so it cannot be checked against anything. It lives here
// so the CLI and the pager state the one MEASURED figure identically.
func ExactAmount(n int) string {
	sign := ""
	if n < 0 {
		sign, n = "-", -n
	}
	digits := strconv.Itoa(n)
	var b strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return sign + b.String()
}
