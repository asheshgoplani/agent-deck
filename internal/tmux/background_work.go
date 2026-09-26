package tmux

import (
	"regexp"
	"strings"
)

// Claude Code prints two different "not finished yet" indicators at the
// prompt after the foreground turn ends. They mean different things for the
// status light, so they are matched separately.
//
// claudeAwaitedAgentRe: the turn ended by handing off to a background agent
// and Claude resumes on its own when that agent reports back:
//
//	✻ Waiting for 1 background agent to finish
//
// Nothing the operator types here is needed, so the session stays green.
//
// claudeBackgroundShellRe: run_in_background shells or a Monitor left behind
// by a turn that is otherwise complete:
//
//	✻ Churned for 6m 24s · done 4:36 PM · 2 shells still running
//	✻ Baked for 13s · done 2:02 PM · 1 monitor still running
//	⏵⏵ bypass permissions on · 2 shells · ← for agents   (footer; segment present iff shells>0)
//
// Those shells can run for hours (dev servers, tail -f, test runs on another
// box, hung monitors) after the model has printed its completion sentinel and
// gone back to the prompt. The session is waiting for input; the operator can
// and should act on it. Before the status-detection audit of 2026-09-23 this
// case was mapped to running, which is exactly the false green the audit was
// opened for (6 of 6 "running" rows on one remote host were this). It now
// stays waiting and is reported through SubstateBackgroundWork instead.
var (
	claudeAwaitedAgentRe    = regexp.MustCompile(`(?i)waiting\s+for\s+\d+\s+background\s+agents?\s+to\s+finish`)
	claudeBackgroundShellRe = regexp.MustCompile(`(?i)` +
		`\d+\s+(?:shells?|monitors?)\s+still\s+running` + // completion line
		`|·\s*\d+\s+(?:shells?|monitors?)\s*·`) // footer counter
)

// backgroundWorkScanLines bounds the scan to the pane tail (completion line +
// input box + footer) so a transcript that merely mentions "shells" in prose
// further up the scrollback cannot trip the detector.
const backgroundWorkScanLines = 20

// claudeBackgroundWorkPending reports whether the (ANSI-stripped) Claude pane
// shows a turn that is still owed a background agent's result, i.e. Claude
// will resume without operator input. Pure and Claude-shaped; callers gate it
// to Claude sessions.
func claudeBackgroundWorkPending(content string) bool {
	return paneTailMatches(content, claudeAwaitedAgentRe)
}

// claudeBackgroundShellsPending reports whether the (ANSI-stripped) Claude
// pane shows background shells or monitors still alive at the prompt. This is
// informational (SubstateBackgroundWork); it never makes the session running.
func claudeBackgroundShellsPending(content string) bool {
	return paneTailMatches(content, claudeBackgroundShellRe)
}

// paneTailMatches reports whether re matches within the last
// backgroundWorkScanLines lines of content.
func paneTailMatches(content string, re *regexp.Regexp) bool {
	if content == "" {
		return false
	}
	recent := strings.Join(lastNLines(content, backgroundWorkScanLines), "\n")
	return re.MatchString(recent)
}
