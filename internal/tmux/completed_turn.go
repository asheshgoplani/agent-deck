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
// That line is the newest structural evidence a pane can carry about a turn:
// everything above it belongs to an earlier turn. Two consumers rely on it:
//
//   - the banner scans (auth-401 / model-unavailable) stop at it, so a banner
//     the session has since recovered from is history, not state (defect C);
//   - the hook-lag rule in session.UpdateStatus, which needs to know that the
//     pane shows a FINISHED turn at an idle prompt while the lifecycle hook
//     still says running (defect B).
//
// The zero-duration "Crunched for 0s" line is Claude's no-op completion (the
// model-unavailable loop) and is deliberately NOT a completed turn.

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
// picker, "N shells still running", or any spinner all return false — those
// panes may legitimately still be working or blocked, and this verdict is
// used to overrule a hook, so it must only fire on the unambiguous frame.
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

// CompletedTurnSampleInterval bounds how often CompletedTurnAtIdlePrompt
// captures the pane for a session whose hook says running. Each capture is one
// "pass" for the hook-lag rule, so two confirming passes are at least this far
// apart — a single frame can never flip the light. Same ceiling as the
// background-work probe on the waiting path.
const CompletedTurnSampleInterval = bgWorkCacheTTL

// CompletedTurnAtIdlePrompt captures the pane (at most once per
// CompletedTurnSampleInterval) and reports whether it shows a finished Claude
// turn at an idle prompt. sampled is true only when THIS call captured and
// classified the pane; a call served from the cache returns the previous
// verdict with sampled=false so the caller does not count it as a new pass.
// The captured frame also refreshes the cached substate, so a TUI row can show
// the verdict without a second capture. Safe to call without holding s.mu.
func (s *Session) CompletedTurnAtIdlePrompt() (idle, sampled bool) {
	s.mu.Lock()
	if !s.isClaudeTool() {
		s.mu.Unlock()
		return false, false
	}
	if !s.completedTurnCheckedAt.IsZero() && time.Since(s.completedTurnCheckedAt) < CompletedTurnSampleInterval {
		idle = s.completedTurnIdle
		s.mu.Unlock()
		return idle, false
	}
	s.mu.Unlock()

	rawContent, err := s.CapturePane()
	if err != nil {
		// A failed capture is not evidence of anything; keep the previous
		// verdict and leave the timestamp alone so the next call retries.
		s.mu.Lock()
		idle = s.completedTurnIdle
		s.mu.Unlock()
		return idle, false
	}
	content := StripANSI(rawContent)

	s.mu.Lock()
	defer s.mu.Unlock()
	// classifySubstate owns the cached detector; it leaves it nil only when the
	// tool cannot be inferred, which is not a Claude pane.
	s.lastSubstate = s.classifySubstate(content)
	s.lastSubstateDetail = s.substateDetailLocked(content)
	idle = s.cachedPromptDetector != nil && s.cachedPromptDetector.CompletedTurnAtIdlePrompt(content)
	s.completedTurnIdle = idle
	s.completedTurnCheckedAt = time.Now()
	return idle, true
}
