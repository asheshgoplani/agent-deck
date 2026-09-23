package tmux

import (
	"regexp"
	"strings"
	"time"
)

// Completed-turn detection (status-light audit 2026-09-17, defects B and C).
//
// Claude Code closes every turn with a summary line led by one of its
// asterisk glyphs and a non-zero duration:
//
//	✻ Sautéed for 3m 4s · done 9:08 PM
//	✻ Worked for 2s · done 8:26 PM
//	✶ Crunched for 12s (1.2k tokens)
//
// Two consumers rely on turn structure:
//
//   - the banner scans (auth-401 / model-unavailable) only look at the LAST
//     turn: a banner above a LATER submitted prompt is history, not state
//     (defect C). The summary line itself is NOT a boundary — a turn that
//     fails after doing work prints the banner and then its summary
//     ("⏺ Please run /login · API Error: 401 …" / "✻ Worked for 45s · done"),
//     and that banner is current;
//   - the hook-lag rule in session.UpdateStatus, which needs to know that the
//     pane shows a FINISHED turn at an idle prompt while the lifecycle hook
//     still says running (defect B).
//
// The zero-duration "Crunched for 0s" line is Claude's no-op completion (the
// model-unavailable loop) and is deliberately NOT a completed turn.

// claudePromptGlyphs lead the input box and the echoed copy of a submitted
// prompt ("❯ " on current Claude Code, "> " on older builds).
var claudePromptGlyphs = []string{"❯", ">"}

// isClaudePromptLine reports whether the (trimmed) line is led by a prompt
// glyph, and whether it carries typed text after it.
func isClaudePromptLine(line string) (isPrompt, hasText bool) {
	for _, glyph := range claudePromptGlyphs {
		if !strings.HasPrefix(line, glyph) {
			continue
		}
		rest := strings.TrimSpace(strings.ReplaceAll(strings.TrimPrefix(line, glyph), "\u00A0", " "))
		return true, rest != ""
	}
	return false, false
}

// forEachCurrentTurnLine walks the last n non-empty lines of content from the
// bottom up and calls fn on each line that belongs to the LAST turn. It stops
// (without calling fn) at the first submitted prompt — the echoed "❯ <text>"
// that started the newest turn — because everything above it belongs to an
// earlier turn: a banner or no-op line up there has already been answered by
// a later prompt, so it is history. The bottom-most prompt line is the input
// box (Claude Code always draws it last, with only footer lines below), so
// typed-but-unsent text there is never mistaken for a submitted prompt.
//
// fn returns true to stop early. The walk reports whether fn stopped it.
func forEachCurrentTurnLine(content string, n int, fn func(line string) bool) bool {
	lines := strings.Split(content, "\n")
	checked := 0
	seenInputBox := false
	for i := len(lines) - 1; i >= 0 && checked < n; i-- {
		line := strings.TrimSpace(StripANSI(lines[i]))
		if line == "" {
			continue
		}
		checked++
		if isPrompt, hasText := isClaudePromptLine(line); isPrompt {
			if seenInputBox && hasText {
				return false // a later turn started here; above is history
			}
			seenInputBox = true
		}
		if fn(line) {
			return true
		}
	}
	return false
}

// claudeCompletedTurnRe matches the turn-summary line and captures its
// duration. Anchored at line start on the glyph so prose quoting the phrase
// ("it said Worked for 2s") cannot match.
var claudeCompletedTurnRe = regexp.MustCompile(`^[✳✽✶✻✢]\s*\S+ for (\d+[hms](?:\s\d+[hms])*)\b`)

// isClaudeCompletedTurnLine reports whether the (trimmed, ANSI-stripped) line
// is a non-zero-duration turn summary.
func isClaudeCompletedTurnLine(line string) bool {
	m := claudeCompletedTurnRe.FindStringSubmatch(line)
	if m == nil {
		return false
	}
	return m[1] != "0s"
}

// hasClaudeCompletedTurn reports whether the recent tail carries a completed
// turn summary line.
func hasClaudeCompletedTurn(content string) bool {
	for _, line := range lastNLines(content, 15) {
		if isClaudeCompletedTurnLine(strings.TrimSpace(StripANSI(line))) {
			return true
		}
	}
	return false
}

// hasClaudeEmptyPromptLine reports whether the recent tail carries a bare
// input prompt ("❯" / ">" with nothing typed). Scans the last 8 non-empty
// lines: Claude Code draws up to seven footer lines under the input box
// (rule, status line, mode line, update notice, compaction hint, /rc).
func hasClaudeEmptyPromptLine(content string) bool {
	lines := strings.Split(content, "\n")
	checked := 0
	for i := len(lines) - 1; i >= 0 && checked < 8; i-- {
		line := strings.ReplaceAll(strings.TrimSpace(StripANSI(lines[i])), "\u00A0", " ")
		if line == "" {
			continue
		}
		checked++
		if line == ">" || line == "❯" {
			return true
		}
	}
	return false
}

// CompletedTurnAtIdlePrompt reports whether the pane shows a FINISHED Claude
// turn sitting at an EMPTY prompt with nothing in flight: a completed-turn
// summary in the recent tail, a bare input prompt, no busy cue, no open menu
// and no pending background work. It is the pane half of the hook-lag rule
// (session.UpdateStatus): a lifecycle hook that still says "running" over
// this frame is lagging. Claude-only; every other tool returns false.
//
// Deliberately strict. A typed-but-unsent prompt, a survey or permission
// picker, an awaited background agent, or any spinner all return false —
// those panes may legitimately still be working or blocked, and this verdict
// is used to overrule a hook, so it must only fire on the unambiguous frame.
// Background shells left alive at the prompt do NOT keep the turn open.
func (d *PromptDetector) CompletedTurnAtIdlePrompt(content string) bool {
	if d.tool != "claude" {
		return false
	}
	if d.hasClaudeBusyIndicator(content) {
		return false
	}
	if !hasClaudeCompletedTurn(content) {
		return false
	}
	if !hasClaudeEmptyPromptLine(content) {
		return false
	}
	if hasOpenInteractiveMenu(content) {
		return false
	}
	return !claudeBackgroundWorkPending(content)
}

// CompletedTurnSampleInterval is the minimum spacing between two pane
// samples for the hook-lag rule to count them as independent: a single frame
// can never flip the light. Same ceiling as the background-work probe on the
// waiting path.
const CompletedTurnSampleInterval = bgWorkCacheTTL

// recordCompletedTurnSampleLocked stores the completed-turn verdict for a pane
// frame that a status or substate read has ALREADY captured and classified
// (pure string ops; no capture of its own). The hook-lag rule in
// session.UpdateStatus consumes it through CachedCompletedTurnSample, so the
// running hook fast path never has to read the pane itself. Caller holds s.mu;
// classifySubstate leaves the detector nil only when the tool cannot be
// inferred, which is not a Claude pane.
func (s *Session) recordCompletedTurnSampleLocked(content string) {
	s.completedTurnIdle = s.cachedPromptDetector != nil && s.cachedPromptDetector.CompletedTurnAtIdlePrompt(content)
	s.completedTurnSampledAt = time.Now()
}

// CachedCompletedTurnSample returns the last completed-turn verdict recorded
// by a pane read (GetStatus / GetSubstate) and when that frame was captured;
// a zero time means no frame has been classified yet. Never captures.
func (s *Session) CachedCompletedTurnSample() (idle bool, sampledAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.completedTurnIdle, s.completedTurnSampledAt
}
