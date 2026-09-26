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
	// idle but is actually wedged. Pairs with status "idle"/"waiting". Does
	// NOT apply when an interactive menu is open (see SubstateInteractiveMenu).
	SubstateIdleAtEmptyPrompt Substate = "idle-at-empty-prompt"

	// SubstateInteractiveMenu marks a session sitting at an open selection
	// menu (an AskUserQuestion picker or a Yes/No permission dialog) awaiting
	// the operator's choice. Pairs with status "waiting". Before this
	// substate existed, an open menu satisfied the same pane-text checks as a
	// bare empty prompt and was misreported as idle-at-empty-prompt, which
	// reads as "nothing happening" when work is in fact blocked on a pending
	// answer (#2185).
	SubstateInteractiveMenu Substate = "interactive-menu"

	// SubstateBackgroundWork marks a Claude session sitting at its input
	// prompt with run_in_background shells or a Monitor still alive ("N
	// shells still running" / "· N shells ·" in the footer). The turn is
	// done and the session is waiting for input; the shells are context, not
	// activity. See background_work.go for why this is not "running".
	SubstateBackgroundWork Substate = "background-work"

	// SubstateModelUnavailable marks the Fable-down no-op loop: the model
	// reports unavailable ("X is currently unavailable", "Crunched for 0s")
	// and the session cannot make progress despite looking alive. The single
	// most important new signal. Pairs with status "error".
	SubstateModelUnavailable Substate = "model-unavailable"

	// SubstateAuth401 marks an auth/connection failure banner ("Please run
	// /login", "API Error: 401", "socket connection closed"). Pairs with
	// status "error". Built on the #1400 error-banner detection.
	SubstateAuth401 Substate = "auth-401"

	// SubstateUsageLimit marks a session whose plan usage window is exhausted:
	// the pane is healthy and accepts input, but every submitted turn is
	// rejected until the window resets. Pairs with status "idle"/"waiting" —
	// which is precisely why it needs its own signal, since "idle" is the state
	// periodic senders treat as safe to send into.
	//
	// Unlike its neighbours this substate is NOT derived from pane text. The
	// rejection is structured data in the agent's transcript, so the verdict is
	// formed there (see internal/session/usagelimit.go, #1802) and surfaced
	// through Instance.Substate rather than ClassifySubstate.
	SubstateUsageLimit Substate = "usage-limit"

	// SubstateUnknownExit marks a terminated-pane classification made with NO
	// evidence at all: the tmux pane vanished without a captured exit code
	// (no remain-on-exit), and this hook-emitting session never recorded a
	// single hook status either. classifyTerminatedPane still reports the
	// coarse status as "error" (unchanged, historical default — see #2091),
	// but that verdict is a GUESS, not a fact: a crash and a clean exit that
	// raced pane teardown look identical here. This substate is what keeps
	// the guess from rendering as confirmed fact (see
	// pattern_unknown_as_first_class_state) — callers that gate on substate
	// (self-heal, auto-restart) must treat it like auth-401/model-unavailable
	// and hold off rather than act on an unverified crash.
	SubstateUnknownExit Substate = "unknown-exit"

	// SubstateHookLag marks a Claude session whose lifecycle hook still says
	// "running" while the pane has shown a completed turn at an idle prompt
	// (no spinner, no interrupt hint, no background work) on two or more
	// consecutive samples. The Stop hook has not landed yet; the pane is the
	// newer evidence. Pairs with status "running" on the first sample (the
	// light is never flipped on a single pass) and "waiting" once confirmed.
	// Named rather than hidden so the reason for the light is visible
	// (status-light audit 2026-09-17, defect B).
	SubstateHookLag Substate = "hook-lag"
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
// Claude and codex have arms (each reads only its own captured renderings);
// every other tool returns SubstateNone.
//
// Claude precedence (most-actionable first). With only a fixed text window and no
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
//  4. interactive-menu — an open AskUserQuestion picker or permission dialog
//     is on screen. Checked before idle-at-empty-prompt: both conditions make
//     hasClaudePrompt true, but a menu awaiting a choice is blocked-on-input,
//     not idle (#2185).
//  5. idle-at-empty-prompt — sitting at the prompt with nothing happening.
//  6. none      — no distinct refinement.
func (d *PromptDetector) ClassifySubstate(content string) Substate {
	// The gate is explicit per tool: each arm reads only renderings captured
	// from that tool. A tool without an arm stays SubstateNone — unknown is
	// reported as unknown, never guessed from another tool's phrasing.
	switch d.tool {
	case "claude":
		return d.classifyClaudeSubstate(content)
	case "codex":
		return classifyCodexSubstate(content)
	default:
		return SubstateNone
	}
}

// SubstateDetail returns free-text detail for the substate ClassifySubstate
// would return for content, or "" when there is none. Today only the codex
// usage-limit banner carries one: the retry time the banner prints ("try
// again at Oct 10th, 2026 8:03 AM").
func (d *PromptDetector) SubstateDetail(content string) string {
	if d.tool != "codex" {
		return ""
	}
	if kind, detail := scanCodexErrorBanner(content); kind == codexBannerUsageLimit {
		return detail
	}
	return ""
}

// classifyCodexSubstate is the codex arm of ClassifySubstate. Codex has no
// idle-at-empty-prompt heuristic (its composer placeholder is always drawn),
// so the only verdicts are the ones its own banners and pickers spell out.
func classifyCodexSubstate(content string) Substate {
	switch kind, _ := scanCodexErrorBanner(content); kind {
	case codexBannerUsageLimit:
		return SubstateUsageLimit
	case codexBannerAuth:
		return SubstateAuth401
	}
	if hasCodexInteractiveMenu(content) {
		return SubstateInteractiveMenu
	}
	return SubstateNone
}

func (d *PromptDetector) classifyClaudeSubstate(content string) Substate {
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

	// 4/5. Sitting at the input prompt with no busy/error signal. hasClaudePrompt
	//    is also true for an open selection menu (its footer/option text is
	//    what makes the coarse status "waiting" in the first place), so an
	//    open menu must be told apart from a genuinely empty prompt before
	//    defaulting to idle.
	if d.hasClaudePrompt(content) {
		if hasOpenInteractiveMenu(content) {
			return SubstateInteractiveMenu
		}
		if claudeBackgroundShellsPending(content) {
			return SubstateBackgroundWork
		}
		return SubstateIdleAtEmptyPrompt
	}

	return SubstateNone
}

// interactiveMenuMarkers are the footer/option strings Claude Code renders
// for an open selection menu — an AskUserQuestion picker or a permission
// dialog (Yes/No, Allow once/always). These are a subset of the
// permissionPrompts checked by hasClaudePrompt (detector.go), which is why
// such a pane already satisfies hasClaudePrompt: it is genuinely "waiting",
// just not idle. Kept separate from permissionPrompts so this list only
// needs to be unambiguous, not exhaustive — a marker missing here degrades to
// the pre-existing idle-at-empty-prompt label rather than a false positive.
var interactiveMenuMarkers = []string{
	"Use arrow keys to navigate",
	"Press Enter to select",
	"Tab/Arrow keys to navigate",
	"Enter to select",
	"No, and tell Claude what to do differently",
	"Do you want",
	"Would you like",
	"Allow once",
	"Allow always",
	// First-run trust dialog ("❯ No, exit / Yes, I trust this folder"), which
	// renders "Enter to confirm" rather than "Enter to select" (audit E).
	"Enter to confirm",
	"Yes, I trust this folder",
	// End-of-session feedback survey ("1: Bad 2: Fine 3: Good 0: Dismiss"),
	// an open picker drawn above the input box (audit D).
	"How is Claude doing this session",
	"0: Dismiss",
}

// codexInteractiveMenuMarkers are the footer strings codex renders under an
// open picker (model switch on rate limit, approval choices). Captured from
// a live codex session (audit E).
var codexInteractiveMenuMarkers = []string{
	"Press enter to confirm or esc to go back",
}

// hasCodexInteractiveMenu reports whether a codex pane shows an open picker,
// scoped to the recent tail like the Claude check.
func hasCodexInteractiveMenu(content string) bool {
	recent := recentTailLower(content, 15)
	for _, marker := range codexInteractiveMenuMarkers {
		if strings.Contains(recent, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}

// hasOpenInteractiveMenu reports whether the pane shows an open selection
// menu awaiting the operator's choice, scoped to the recent tail so a stale
// menu scrolled out of view does not keep matching forever.
func hasOpenInteractiveMenu(content string) bool {
	recent := recentTailLower(content, 15)
	for _, marker := range interactiveMenuMarkers {
		if strings.Contains(recent, strings.ToLower(marker)) {
			return true
		}
	}
	return false
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
	// ("… (53s · ↓ 749 tokens)") is an unambiguous active-work signal. The
	// timing pattern only counts when "…" and "tokens" share a LINE: an idle
	// footer routinely carries both on separate lines ("… +24 lines (ctrl+o
	// to expand)" from a finished tool call, "new task? /clear to save 380k
	// tokens" from the compaction hint), which misread as running on 11 of 92
	// audited sessions (audit F). Same rule as hasInterruptBusyContext.
	hasActiveCue := strings.Contains(recent, "ctrl+c to interrupt") ||
		strings.Contains(recent, "esc to interrupt") ||
		hasSameLineTimingCue(recent)
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
// match. Only the last turn is read (forEachCurrentTurnLine): a no-op line
// above a later submitted prompt is history, not state (audit C).
func hasModelUnavailableNoop(content string) bool {
	return forEachCurrentTurnLine(content, 15, func(line string) bool {
		// Skip quoted/input lines (user typing ABOUT a model being unavailable,
		// or a tool result quoting another session). Mirrors the banner guard.
		if hasAnyPrefix(line, claudeQuotedLinePrefixes) {
			return false
		}
		return strings.Contains(line, crunchedNoopMarker) || containsAny(line, modelUnavailableSubstrings)
	})
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

// hasSameLineTimingCue reports whether any line of the (lowercased) tail carries
// Claude's live timing readout — the unicode ellipsis and the token counter on
// ONE line ("✢ hullaballooing… (53s · ↓ 749 tokens)"). The two fragments on
// different lines are the idle footer, not work.
func hasSameLineTimingCue(tail string) bool {
	for _, line := range strings.Split(tail, "\n") {
		if strings.Contains(line, "…") && strings.Contains(line, "tokens") {
			return true
		}
	}
	return false
}
