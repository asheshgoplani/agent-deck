package tmux

import (
	"regexp"
	"strings"
)

// FrameVerdict is what one captured pane frame says on its own, with no
// history (no spinner grace, no acknowledgement, no debounce): the verdict
// GetStatus would reach for that frame on a first call.
type FrameVerdict string

const (
	FrameActive  FrameVerdict = "active"
	FrameWaiting FrameVerdict = "waiting"
	FrameError   FrameVerdict = "error"
	// FrameUnknown means the frame carries no recognised cue at all;
	// GetStatus then falls back to history (last stable status / waiting).
	FrameUnknown FrameVerdict = "unknown"
)

// ClassifyPaneFrame runs the pane-tail detectors of GetStatus over one frame
// for the given tool and returns the frame-only verdict, in GetStatus order:
// model-unavailable no-op → error banner → busy indicator → background work
// → prompt indicator. It is the scoring oracle for the golden pane corpus
// (testdata/status_corpus) and shares every helper with GetStatus, so a
// detector change is scored against real frames before it ships.
func ClassifyPaneFrame(tool, content string) FrameVerdict {
	s := &Session{DisplayName: "frame", detectedTool: strings.ToLower(strings.TrimSpace(tool))}
	content = s.prepareFrame(StripANSI(content))
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.classifyFrameLocked(content) == SubstateModelUnavailable {
		return FrameError
	}
	if s.hasErrorBannerIndicator(content) {
		return FrameError
	}
	if s.hasBusyIndicator(content) {
		return FrameActive
	}
	if s.markBackgroundWorkActiveLocked(content, 0, s.DisplayName) {
		return FrameActive
	}
	if s.hasPromptIndicator(content) {
		return FrameWaiting
	}
	return FrameUnknown
}

// prepareFrame is the per-tool normalisation GetStatus applies to a captured
// (ANSI-stripped) frame before any window-bounded detector looks at it.
//
// Claude: the agent roster ("⏺ main" / "◯ general-purpose  Task …", one row per
// sub-agent) and the artifact list ("⧉  name") are drawn BELOW the footer.
// The spinner scan covers the whole pane, but the tail-bounded detectors do
// not: the background-work scan (20 lines), the prompt (8) and the menu (15).
// A turn handed off to background agents ("✻ Waiting for 3 background agents
// to finish") is exactly the frame with one roster row per agent, and on a
// long roster that line fell out of its window and the session read waiting
// while Claude was still owed the results. Those trailing rows say nothing
// about the turn, so they are cut. GetStatus, GetSubstate and
// BackgroundWorkPending (the Stop-hook path) all read the trimmed frame.
func (s *Session) prepareFrame(content string) string {
	if !s.isClaudeTool() {
		return content
	}
	return trimClaudeTrailingRoster(content)
}

// claudeFooterRe matches the mode line under Claude's input box, the last line
// of the frame proper.
var claudeFooterRe = regexp.MustCompile(`^\s*⏵⏵ |shift\+tab to cycle|← for agents`)

// claudeRosterRowRe matches the rows Claude appends under the footer: agent
// roster entries, the artifact list and its "+N more" continuation.
var claudeRosterRowRe = regexp.MustCompile(`^\s*(?:[⏺◯●]\s|⧉\s|\+\d+ more\b)`)

// trimClaudeTrailingRoster drops agent-roster / artifact rows that follow the
// footer. Anything else after the footer (a menu, an error line) keeps the
// frame intact, so the trim can only ever remove rows the detectors ignore.
func trimClaudeTrailingRoster(content string) string {
	lines := strings.Split(content, "\n")
	footer := -1
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-60; i-- {
		if claudeFooterRe.MatchString(lines[i]) {
			footer = i
			break
		}
	}
	if footer < 0 || footer == len(lines)-1 {
		return content
	}
	for _, l := range lines[footer+1:] {
		if strings.TrimSpace(l) == "" || claudeRosterRowRe.MatchString(l) {
			continue
		}
		return content
	}
	return strings.Join(lines[:footer+1], "\n")
}

// claudeTailBusyRe is the unambiguous live-work cue inside the recent tail:
// an interrupt hint or a spinner line with the "(Ns · …)" timing group.
var claudeTailBusyRe = regexp.MustCompile(`(?i)esc to interrupt|ctrl\+c to interrupt|^[✳✽✶✻✢·⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏]\s*\S.*…\s*\(`)

// claudeMenuOutranksBusy reports whether a Claude frame ends in an OPEN
// selection menu (AskUserQuestion, permission, trust, feedback picker) with
// no live busy cue near it. A menu blocks the turn until the operator picks,
// so it is waiting even when a spinner line frozen further up the pane
// ("✶ Kerfuffling… (3s · thinking)" left where the question interrupted the
// turn) would otherwise read as running: the whole-pane spinner scan
// (findSpinnerInContent) has no distance limit.
func (s *Session) claudeMenuOutranksBusy(content string) bool {
	if !s.isClaudeTool() || !hasOpenInteractiveMenu(content) {
		return false
	}
	for _, line := range lastNLines(content, 15) {
		if claudeTailBusyRe.MatchString(strings.TrimSpace(line)) {
			return false
		}
	}
	return true
}
