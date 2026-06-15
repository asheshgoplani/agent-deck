package tmux

import "strings"

// Substate is an ADDITIVE refinement of the coarse session status (Honest
// Status v2). It never changes the canonical status string ("running",
// "waiting", "idle", "error", "stopped"); it explains WHY a session is in that
// status so a supervisor (human or Maestro) can act precisely.
//
// The motivating failure: a Fable no-op loop ("X is currently unavailable" /
// "Crunched for 0s") looked alive to a coarse observer. Distinguishing
// model-unavailable from a genuinely-running session makes "running"
// trustworthy.
type Substate string

const (
	// SubstateNone means no distinct refinement applies (the coarse status is
	// already the whole story, e.g. a session making real progress).
	SubstateNone Substate = ""

	// SubstateRunning marks a session actively working (spinner /
	// "esc|ctrl+c to interrupt" present). Pairs with status "running".
	SubstateRunning Substate = "running"

	// SubstateIdleAtEmptyPrompt marks a session sitting at its input prompt
	// with no activity — genuinely idle, distinct from a session that LOOKS
	// idle but is actually wedged. Pairs with status "idle"/"waiting".
	SubstateIdleAtEmptyPrompt Substate = "idle-at-empty-prompt"

	// SubstateModelUnavailable marks the Fable-down no-op loop: the model
	// reports unavailable ("X is currently unavailable", "Crunched for 0s")
	// and the session cannot make progress despite looking alive. The single
	// most important new signal. Pairs with status "error".
	SubstateModelUnavailable Substate = "model-unavailable"

	// SubstateAuth401 marks an auth/connection failure banner ("Please run
	// /login", "API Error: 401", "socket connection closed"). Pairs with
	// status "error". Built on the #1400 error-banner detection.
	SubstateAuth401 Substate = "auth-401"
)

// modelUnavailableSubstrings are fragments of the Fable/model-down no-op the
// tool renders in the pane. Anchored on the rendered phrasing rather than a
// bare token so ordinary conversation does not match.
var modelUnavailableSubstrings = []string{
	"is currently unavailable",
	"model is currently unavailable",
}

// crunchedNoopMarker matches the "Crunched for 0s" zero-work completion the
// no-op loop emits. The trailing "0s" is what distinguishes a no-op from a real
// (non-zero-duration) crunch.
const crunchedNoopMarker = "Crunched for 0s"

// ClassifySubstate returns the additive Substate for the given pane content.
// Claude-only (the heuristics are Claude Code renderings); other tools return
// SubstateNone.
//
// Precedence (most-actionable first). With only a fixed text window and no
// timestamps, a stale line and a current line cannot be ordered perfectly; this
// ordering picks the verdict that is RIGHT in the realistic case for each pair:
//
//  1. auth-401  — a TERMINAL auth/connection failure banner. Checked FIRST: a
//     401 is unrecoverable and stops the spinner, so when the banner is present
//     a busy cue elsewhere in the window is the stale one. hasClaudeErrorBanner
//     already excludes prose / quoted (⎿) / input-line / behind-spinner-retry
//     banners, so an in-flight retry (which IS still working) does not match
//     here and correctly falls through to the busy check below.
//  2. running   — a genuine, UNAMBIGUOUS active-work cue (interrupt hint /
//     braille spinner / "… tokens" timing). Wins over a stale model-unavailable
//     no-op: if the session is crunching NOW, an older "Crunched for 0s" /
//     "unavailable" line is stale. Deliberately does NOT treat a bare "✶" as a
//     cue, so the no-op completion line's decorative asterisk does not match.
//  3. model-unavailable — the Fable-down no-op loop with no live busy cue.
//  4. idle-at-empty-prompt — sitting at the prompt with nothing happening.
//  5. none      — no distinct refinement.
func (d *PromptDetector) ClassifySubstate(content string) Substate {
	if d.tool != "claude" {
		return SubstateNone
	}

	// 1. Terminal auth/connection failure banner (#1400 heuristic, with its
	//    prose/quoted/retry-behind-spinner over-match guards already baked in).
	//    A genuine banner means the session is wedged; it outranks a stale busy
	//    cue. A retry-in-progress is quoted behind "⎿" and excluded, so it
	//    falls through to the busy check and stays "running".
	if hasClaudeErrorBanner(content) {
		return SubstateAuth401
	}

	// 2. A genuine, unambiguous active-work cue wins over a stale no-op.
	if d.hasClaudeBusyIndicator(content) {
		return SubstateRunning
	}

	// 3. Model-unavailable no-op loop (Fable down) with no live busy cue: the
	//    "Crunched for 0s" / "is currently unavailable" line is the actionable
	//    signal. Scan the recent tail so a stale line scrolled far up does not
	//    match.
	if hasModelUnavailableNoop(content) {
		return SubstateModelUnavailable
	}

	// 4. Sitting at the input prompt with no busy/error signal = genuinely idle.
	if d.hasClaudePrompt(content) {
		return SubstateIdleAtEmptyPrompt
	}

	return SubstateNone
}

// hasClaudeBusyIndicator reports whether the recent pane tail shows Claude
// actively working (spinner char or an "esc|ctrl+c to interrupt" hint). It is
// the substate-classification counterpart of the busy checks inside
// hasClaudePrompt, scoped to the same recent-tail window.
func (d *PromptDetector) hasClaudeBusyIndicator(content string) bool {
	// Scope ALL checks to the recent tail: a spinner char left in scrollback
	// must not permanently classify the session as running and mask a later
	// auth/model failure. recentTailLower lowercases, which does not affect the
	// (already non-alphabetic) spinner glyphs.
	recent := recentTailLower(content, 15)
	// An explicit interrupt hint or the whimsical-word + timing pattern
	// ("… (53s · ↓ 749 tokens)") is an unambiguous active-work signal.
	hasActiveCue := strings.Contains(recent, "ctrl+c to interrupt") ||
		strings.Contains(recent, "esc to interrupt") ||
		(strings.Contains(recent, "…") && strings.Contains(recent, "tokens"))
	if hasActiveCue {
		return true
	}
	// Braille "dots" spinner chars are genuinely ANIMATED — they only render
	// while Claude is processing, so a recent one means active work on its own.
	brailleSpinners := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	for _, sp := range brailleSpinners {
		if strings.Contains(recent, sp) {
			return true
		}
	}
	// The asterisk glyphs (✳ ✽ ✶ ✢, Claude 2.1.25+) double as the leading mark
	// on COMPLETION lines ("✶ Crunched for 12s"), not just the active spinner.
	// So an asterisk alone is NOT sufficient — only treat it as busy when an
	// active cue co-occurs. (The zero-work "Crunched for 0s" no-op is already
	// classified model-unavailable before this function runs.)
	return false
}

// hasModelUnavailableNoop scans the last 15 non-empty lines (same window as the
// error-banner heuristic) for the model-unavailable / zero-work no-op markers.
// Quoted/prompt lines are skipped so prose mentioning "unavailable" does not
// match.
func hasModelUnavailableNoop(content string) bool {
	lines := strings.Split(content, "\n")
	checked := 0
	for i := len(lines) - 1; i >= 0 && checked < 15; i-- {
		line := strings.TrimSpace(StripANSI(lines[i]))
		if line == "" {
			continue
		}
		checked++
		// Skip quoted/input lines (user typing ABOUT a model being unavailable,
		// or a tool result quoting another session). Mirrors the banner guard.
		if hasAnyPrefix(line, claudeQuotedLinePrefixes) {
			continue
		}
		if strings.Contains(line, crunchedNoopMarker) {
			return true
		}
		for _, pat := range modelUnavailableSubstrings {
			if strings.Contains(line, pat) {
				return true
			}
		}
	}
	return false
}

// recentTailLower returns a lowercased join of the last n non-empty lines.
func recentTailLower(content string, n int) string {
	lines := strings.Split(content, "\n")
	var tail []string
	for i := len(lines) - 1; i >= 0 && len(tail) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			tail = append([]string{lines[i]}, tail...)
		}
	}
	return strings.ToLower(strings.Join(tail, "\n"))
}
