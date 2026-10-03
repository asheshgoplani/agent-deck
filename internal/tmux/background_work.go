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
// claudeWorkflowFooterRe / claudeBackgroundToolRe: Claude Code running a background
// Workflow (or background tasks) renders a progress footer line below the prompt:
//
//	○ comms-followon-round3  ▰▰▰▰▰▰▰▰▰▰▰▰▱▱▱▱▱▱▱▱  3/5 · 18m32s · ↓ 784.8k tokens
//
// and above it the tool call `Workflow(...)` with `Running in background · /workflows to monitor`.
// While this work is in flight the session is running with substate background-work.
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

	claudeWorkflowFooterRe = regexp.MustCompile(
		`^[ \t]*[○◯●⏺✻✳*·]?[ \t]*([a-zA-Z0-9_\-\.]+)[ \t]+(?:[▰▱■□]+[ \t]+)?(?:(\d+/\d+)[ \t]*·[ \t]*)?([0-9]+[smhd](?:\s*[0-9]+[smhd])*)`,
	)
	claudeWorkflowStepRe = regexp.MustCompile(`\b(\d+/\d+)\b`)
	claudeElapsedRe      = regexp.MustCompile(`\b([0-9]+[smhd](?:\s*[0-9]+[smhd])*)\b`)
	claudeBackgroundToolRe = regexp.MustCompile(
		`(?i)running\s+in\s+background(?:\s*·\s*/(?:workflows|tasks|processes)\s+to\s+monitor)?`,
	)
	claudeWorkflowCallRe = regexp.MustCompile(
		`(?i)Workflow\(\s*(?:name:\s*)?["']?([a-zA-Z0-9_\-\.]+)`,
	)
	claudeAgentRosterRowRe = regexp.MustCompile(
		`^[ \t]*[◯○]\s+([a-zA-Z0-9_\-\.]+)\s+.*?([0-9]+[smhd](?:\s*[0-9]+[smhd])*)\s*·`,
	)
)

// backgroundWorkScanLines bounds the scan to the pane tail (completion line +
// input box + footer) so a transcript that merely mentions "shells" in prose
// further up the scrollback cannot trip the detector.
const backgroundWorkScanLines = 20

// parseClaudeWorkflowFooter parses the workflow / background task footer
// line if present in the pane tail. Returns matched, task name, step progress,
// and elapsed duration.
func parseClaudeWorkflowFooter(content string) (matched bool, name string, step string, elapsed string) {
	if content == "" {
		return false, "", "", ""
	}
	for _, raw := range lastNLines(content, backgroundWorkScanLines) {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		// Distinctive progress bar glyphs (▰▱■□)
		if strings.ContainsAny(line, "▰▱■□") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				cand := strings.TrimLeft(parts[0], "○◯●⏺✻✳*· ")
				if cand == "" && len(parts) >= 3 {
					cand = parts[1]
				}
				name = cand
				if sm := claudeWorkflowStepRe.FindString(line); sm != "" {
					step = sm
				}
				if em := claudeElapsedRe.FindString(line); em != "" {
					elapsed = em
				}
				return true, name, step, elapsed
			}
		}
		if m := claudeWorkflowFooterRe.FindStringSubmatch(line); len(m) > 1 {
			name = m[1]
			if len(m) > 2 {
				step = m[2]
			}
			if len(m) > 3 {
				elapsed = m[3]
			}
			return true, name, step, elapsed
		}
	}
	return false, "", "", ""
}

// claudeBackgroundWorkPending reports whether the (ANSI-stripped) Claude pane
// shows active background work in flight: a workflow footer, a background tool call,
// or an awaited background agent. Pure and Claude-shaped; callers gate it to Claude sessions.
func claudeBackgroundWorkPending(content string) bool {
	if content == "" {
		return false
	}
	recent := strings.Join(lastNLines(content, backgroundWorkScanLines), "\n")
	if claudeAwaitedAgentRe.MatchString(recent) {
		return true
	}
	if ok, _, _, _ := parseClaudeWorkflowFooter(content); ok {
		return true
	}
	if claudeBackgroundToolRe.MatchString(recent) {
		return true
	}
	return false
}

// claudeBackgroundWorkDetail formats the human-readable detail for in-flight
// background work (task name, step progress, elapsed duration) for SubstateDetail.
func claudeBackgroundWorkDetail(content string) string {
	if content == "" {
		return ""
	}
	if ok, name, step, elapsed := parseClaudeWorkflowFooter(content); ok {
		var parts []string
		if name != "" {
			parts = append(parts, name)
		}
		if step != "" && elapsed != "" {
			parts = append(parts, step+" · "+elapsed)
		} else if step != "" {
			parts = append(parts, step)
		} else if elapsed != "" {
			parts = append(parts, elapsed)
		}
		if len(parts) > 0 {
			return strings.Join(parts, " ")
		}
	}
	recent := strings.Join(lastNLines(content, backgroundWorkScanLines), "\n")
	if claudeAwaitedAgentRe.MatchString(recent) {
		for _, line := range lastNLines(content, backgroundWorkScanLines) {
			if m := claudeAgentRosterRowRe.FindStringSubmatch(strings.TrimSpace(line)); len(m) > 2 {
				return m[1] + " " + m[2]
			}
		}
		return strings.TrimSpace(claudeAwaitedAgentRe.FindString(recent))
	}
	if claudeBackgroundToolRe.MatchString(recent) {
		if m := claudeWorkflowCallRe.FindStringSubmatch(recent); len(m) > 1 {
			return m[1] + " (running in background)"
		}
		return "running in background"
	}
	return ""
}

// claudeBackgroundTaskName returns just the task name of the background task if found.
func claudeBackgroundTaskName(content string) string {
	if content == "" {
		return ""
	}
	if ok, name, _, _ := parseClaudeWorkflowFooter(content); ok && name != "" {
		return name
	}
	recent := strings.Join(lastNLines(content, backgroundWorkScanLines), "\n")
	if m := claudeWorkflowCallRe.FindStringSubmatch(recent); len(m) > 1 {
		return m[1]
	}
	for _, line := range lastNLines(content, backgroundWorkScanLines) {
		if m := claudeAgentRosterRowRe.FindStringSubmatch(strings.TrimSpace(line)); len(m) > 1 {
			return m[1]
		}
	}
	return ""
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
