package tmux

import (
	"regexp"
	"strings"
)

// claudeBackgroundWorkRe matches Claude Code's at-the-prompt indicators that the
// FOREGROUND turn finished but background work is still in flight. Claude prints
// these on the turn-completion (✻) summary line and in the footer:
//
//	✻ Churned for 6m 24s · 2 shells still running
//	✻ Waiting for 1 background agent to finish
//	⏵⏵ bypass permissions on · 2 shells · ← for agents   (footer; segment present iff shells>0)
//
// run_in_background shells and a background agent the turn awaits are the two
// "still working after Stop" cases. Without recognizing them, agent-deck maps
// Claude's Stop hook to "waiting" (yellow) and fires a premature "finished"
// notification while work is still running. See the background-work-stop-signal
// investigation (issue: bg-only sessions flagged yellow + notified).
var claudeBackgroundWorkRe = regexp.MustCompile(`(?i)` +
	`\d+\s+shells?\s+still\s+running` + // completion line: shells
	`|waiting\s+for\s+\d+\s+background\s+agents?\s+to\s+finish` + // completion line: background agent
	`|·\s*\d+\s+shells?\s*·`) // footer shell counter

// backgroundWorkScanLines bounds the scan to the pane tail (completion line +
// input box + footer) so a transcript that merely mentions "shells" in prose
// further up the scrollback cannot trip the detector.
const backgroundWorkScanLines = 20

// claudeBackgroundWorkPending reports whether the (ANSI-stripped) Claude pane
// content shows in-flight background work at the prompt. Pure and Claude-shaped;
// callers gate it to Claude sessions.
func claudeBackgroundWorkPending(content string) bool {
	if content == "" {
		return false
	}
	recent := strings.Join(lastNLines(content, backgroundWorkScanLines), "\n")
	return claudeBackgroundWorkRe.MatchString(recent)
}
