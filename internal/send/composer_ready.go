package send

import (
	"regexp"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Refusal codes reported by ClaudeComposerReady and ComposerEmptyWhileBusy.
// They are part of the JSON contract of `agent-deck session command`, so keep
// them stable.
const (
	ComposerCodeDraft    = "composer_draft" // operator text sits unsent in the composer
	ComposerCodeBusy     = "agent_busy"     // a turn is running
	ComposerCodeDialog   = "dialog_open"    // a selection dialog owns the keyboard
	ComposerCodeNoPrompt = "no_prompt"      // no Claude composer on screen
)

// busyLookback is how many lines above the composer the running-turn hint
// ("esc to interrupt") is searched for. The spinner line sits right above the
// composer; older transcript text that merely mentions the hint must not
// block delivery forever.
const busyLookback = 12

// ComposerReadiness is the verdict of ClaudeComposerReady and
// ComposerEmptyWhileBusy.
type ComposerReadiness struct {
	Ready bool
	// Code is one of the ComposerCode* constants when Ready is false.
	Code string
	// Ghost reports that the empty composer shows a suggestion or idle hint.
	// Typing replaces it; it must never be accepted (no Tab, no Right).
	Ghost bool
	// DraftLen is the rune length of an operator draft (the text itself is
	// never echoed into results or logs).
	DraftLen int
}

// ClaudeComposerReady is the shared "composer is empty and idle" check every
// automated keystroke path into a Claude pane must pass before typing:
// session command (slash commands) and guarded sends. raw is the pane capture
// with ANSI intact (CapturePaneFresh); the dim attribute is what tells a
// ghost suggestion apart from operator text.
//
// It refuses, in order: a selection dialog (Enter would pick an entry), a
// running turn, a pane without a composer, and a composer holding operator
// text. A ghost suggestion or the "Try ..." hint count as empty.
func ClaudeComposerReady(raw string) ComposerReadiness {
	return composerReadiness(raw, false)
}

// ComposerEmptyWhileBusy is ClaudeComposerReady for a harness that queues
// typed input while a turn runs (session.AcceptsInputWhileBusy): a running
// turn is not a refusal, every other reason still is. The composer must be
// framed (borders above and below the prompt line, as Claude draws it in
// idle and busy screens), so a prompt glyph in scrolled output is never
// taken for the input box.
func ComposerEmptyWhileBusy(raw string) ComposerReadiness {
	return composerReadiness(raw, true)
}

func composerReadiness(raw string, busyOK bool) ComposerReadiness {
	content := tmux.StripANSI(raw)
	if DialogOpen(content) {
		return ComposerReadiness{Code: ComposerCodeDialog}
	}
	if !busyOK && turnRunning(content) {
		return ComposerReadiness{Code: ComposerCodeBusy}
	}
	if busyOK {
		// Busy input needs the framed composer, not a bare prompt glyph.
		if _, framed := framedComposerPrompt(composerSearchLines(content)); !framed {
			return ComposerReadiness{Code: ComposerCodeNoPrompt}
		}
	}
	draft, visible := ComposerDraft(raw, tmux.StripANSI)
	if !visible {
		return ComposerReadiness{Code: ComposerCodeNoPrompt}
	}
	if draft != "" {
		return ComposerReadiness{Code: ComposerCodeDraft, DraftLen: len([]rune(draft))}
	}
	body, _ := CurrentComposerPrompt(content)
	return ComposerReadiness{Ready: true, Ghost: body != ""}
}

// ClaudeTurnRunning reports whether the pane (raw capture) shows a running
// turn next to the composer. Callers that typed into an idle composer re-check
// this right before pressing Enter.
func ClaudeTurnRunning(raw string) bool {
	return turnRunning(tmux.StripANSI(raw))
}

// DialogSelection returns the highlighted option of a numbered selection
// dialog at the bottom of the pane ("❯ 1. Yes, switch to Fable 5.1" in the
// /model "Switch model?" confirmation), and whether one is open.
func DialogSelection(content string) (string, bool) {
	for _, line := range tailLines(content, 10) {
		if m := selectedOption.FindStringSubmatch(line); m != nil {
			return strings.TrimSpace(m[1]), true
		}
	}
	return "", false
}

var selectedOption = regexp.MustCompile(`^\s*❯\s*\d+\.\s+(.+)$`)

// DialogOpen reports a Claude selection dialog (folder trust, /model picker,
// "Switch model?", permission prompts) in the bottom lines of stripped pane
// content.
func DialogOpen(content string) bool {
	if _, ok := DialogSelection(content); ok {
		return true
	}
	for _, line := range tailLines(content, 8) {
		l := strings.ToLower(line)
		if strings.Contains(l, "enter to confirm") || strings.Contains(l, "esc to cancel") {
			return true
		}
	}
	return false
}

// ComposerBounds returns the line indexes of the composer's top and bottom
// borders (the last two border lines in lines), or -1, -1 when the pane has
// no border-framed composer.
func ComposerBounds(lines []string) (top, bottom int) {
	bottom = -1
	for i := len(lines) - 1; i >= 0; i-- {
		if !IsComposerBorderLine(lines[i]) {
			continue
		}
		if bottom < 0 {
			bottom = i
			continue
		}
		return i, bottom
	}
	return -1, -1
}

// turnRunning looks for the running-turn hint near the composer: in the
// footer below it, or within busyLookback lines above its top border.
func turnRunning(content string) bool {
	lines := strings.Split(content, "\n")
	from := len(lines) - busyLookback
	if top, _ := ComposerBounds(lines); top >= 0 {
		from = top - busyLookback
	}
	if from < 0 {
		from = 0
	}
	window := lines[from:]
	if paneLooksBusy(strings.Join(window, "\n")) {
		return true
	}
	for _, line := range window {
		if spinnerLine.MatchString(line) {
			return true
		}
	}
	return false
}

// spinnerLine matches Claude 2.1.296's running-turn status line, e.g.
// "✻ Skedaddling… (1h 1m 26s · ↓ 148.6k tokens)". It carries no
// "esc to interrupt" hint. The finished form ("✻ Brewed for 6s · done
// 4:11 PM") has no ellipsis and does not match.
var spinnerLine = regexp.MustCompile(`^\s*\S\s+[A-Z][\p{L}'-]*…\s*\(\d`)

// IsComposerBorderLine reports a composer border: a plain divider, or the
// top border Claude 2.1.296 draws with the session name in it
// ("──────── my-session ─").
func IsComposerBorderLine(line string) bool {
	return IsComposerDividerLine(line) || strings.HasPrefix(strings.TrimSpace(line), strings.Repeat("─", 10))
}

func tailLines(content string, n int) []string {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}
