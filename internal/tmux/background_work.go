package tmux

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Background work in a Claude pane (issue #2473).
//
// Rule in one line: a running workflow means a running session. Claude Code
// can end its foreground turn and go back to an empty prompt while work it
// started keeps running: a Workflow, background agents, run_in_background
// shells and Monitors. Until that work reports back (a <task-notification>
// turn), the session is RUNNING with substate background-work, whatever the
// prompt or the Stop hook say. Only once nothing is in flight does the
// session settle to waiting (and to idle after the operator acknowledged it).
//
// This reverses the 2026-09-23 audit ruling that read shells / Monitors left
// at the prompt as "waiting, the shells are context, not activity". Ashesh's
// ruling in #2473: work that is in flight is activity.
//
// The pane shows the work in four shapes (Claude Code 2.1.288, captured live
// into testdata and the fixtures in background_work_test.go):
//
//	◯ comms-followon-round3  ▰▰▰▰▰▰▱▱▱▱  3/5 · 18m32s · ↓ 784.8k tokens   (workflow row under the footer)
//	✻ Waiting for 1 dynamic workflow to finish                            (turn handed off, above the input box)
//	✻ Waiting for 3 background agents to finish                           (same, for agents)
//	⏵⏵ bypass permissions on · 2 shells, 1 monitor · ← for agents         (live counter in the footer)
//
// Each one is matched only where Claude draws it, so the same words in
// scrollback prose never count:
//
//   - the workflow row is in flight only while step < steps. A finished
//     workflow keeps its row at n/n (it lingers until the next turn redraws
//     the footer), which is exactly the "done" signal.
//   - the "Waiting for N … to finish" line counts only while it is the last
//     turn line above the input box. Once the background work reports back,
//     Claude prints the notification turn BELOW it, so an older Waiting line
//     still inside the scan window is history, not state.
//   - the shell / monitor counter counts only on the footer line, which
//     Claude redraws live (the "N shells still running" text on a turn's
//     completion line is printed once and goes stale, so it is not used).
//
// The detector reads the pane frame BEFORE the agent-roster trim
// (trimClaudeTrailingRoster): the workflow row is drawn under the footer
// with the same glyph as a roster row, and the trim removes it.
var (
	claudeAwaitedAgentRe = regexp.MustCompile(`(?i)waiting\s+for\s+(\d+)\s+((?:background\s+|dynamic\s+)?(?:agents?|workflows?|tasks?))\s+to\s+finish`)

	// claudeWorkflowRowRe is the workflow progress row. The bar and the
	// tokens segment are optional (narrow panes drop the bar; an idle
	// workflow has no token count yet); the glyph varies with the spinner
	// state (○ ◯ ◐ ◑ ◒ ◓ ● ◉ ◌).
	claudeWorkflowRowRe = regexp.MustCompile(`^[○◯◐◑◒◓●◉◌]\s+(\S(?:.*?\S)?)\s+(?:[▰▱]+\s+)?(\d+)/(\d+)\s+·\s+(\d+[hms](?:\s?\d+[hms])*)\s*(?:·.*)?$`)

	// claudeFooterCounterRe is the live shell / monitor counter segment of
	// the footer ("· 1 shell ·", "· 2 shells, 1 monitor ·", "· 1 monitor").
	claudeFooterCounterRe = regexp.MustCompile(`(?i)(?:^|·)\s*(\d+\s+(?:shells?|monitors?)(?:\s*,\s*\d+\s+(?:shells?|monitors?))*)\s*(?:·|$)`)
	claudeCounterPartRe   = regexp.MustCompile(`(?i)(\d+)\s+(shells?|monitors?)`)

	// claudeFooterLineRe identifies the mode/footer line under the input box.
	claudeFooterLineRe = regexp.MustCompile(`⏵⏵|shift\+tab to cycle|← for agents|for shortcuts`)
)

// Background work kinds carried on BackgroundWork.Kind.
const (
	BackgroundKindWorkflow = "workflow"
	BackgroundKindAgent    = "agent"
	BackgroundKindBash     = "bash"
	BackgroundKindMonitor  = "monitor"
)

// backgroundWorkScanLines bounds the scan to the pane tail (completion line +
// input box + footer) so a transcript that merely mentions "shells" in prose
// further up the scrollback cannot trip the detector.
const backgroundWorkScanLines = 20

// backgroundRowScanLines bounds the workflow-row scan. The rows sit under the
// footer, after any agent-roster rows, so the window is wider than the
// 20-line one; the row shape (glyph, n/m, elapsed) is specific enough that
// prose cannot match it.
const backgroundRowScanLines = 40

// backgroundPromptScanLines bounds the search for the input box from the
// bottom of the pane (footer, status line and up to ~70 roster rows below it).
const backgroundPromptScanLines = 80

// BackgroundWork describes the background work a Claude session has in
// flight. The zero value means nothing is in flight. It is what
// `session show --json` reports as background_work and what the TUI detail
// line renders.
type BackgroundWork struct {
	// Kind is one of the BackgroundKind* constants, "" when nothing is in flight.
	Kind string `json:"kind,omitempty"`
	// Task is the workflow name, agent / command description, or a count
	// summary ("2 shells, 1 monitor") when no name is known.
	Task string `json:"task,omitempty"`
	// Step / Steps is the workflow progress (n/m); zero when not a workflow.
	Step  int `json:"step,omitempty"`
	Steps int `json:"steps,omitempty"`
	// Elapsed is the workflow's elapsed time as Claude renders it ("18m32s").
	Elapsed string `json:"elapsed,omitempty"`
	// Source is "pane", "transcript" or "pane+transcript": which evidence
	// proved the work in flight.
	Source string `json:"source,omitempty"`
}

// MarshalJSON keeps "step" whenever the work has steps: a workflow at 0/5
// reads {"step":0,"steps":5}, not a bare "steps". Other kinds omit both.
func (b BackgroundWork) MarshalJSON() ([]byte, error) {
	var step *int
	if b.Steps > 0 {
		n := b.Step
		step = &n
	}
	return json.Marshal(struct {
		Kind    string `json:"kind,omitempty"`
		Task    string `json:"task,omitempty"`
		Step    *int   `json:"step,omitempty"`
		Steps   int    `json:"steps,omitempty"`
		Elapsed string `json:"elapsed,omitempty"`
		Source  string `json:"source,omitempty"`
	}{b.Kind, b.Task, step, b.Steps, b.Elapsed, b.Source})
}

// InFlight reports whether b describes work still running.
func (b BackgroundWork) InFlight() bool { return b.Kind != "" }

// Summary is the one-line human form, e.g.
// "workflow comms-followon-round3 3/5 · 18m32s". "" when nothing is in flight.
func (b BackgroundWork) Summary() string {
	if !b.InFlight() {
		return ""
	}
	parts := []string{b.Kind}
	if b.Task != "" {
		parts = append(parts, b.Task)
	}
	s := strings.Join(parts, " ")
	if b.Steps > 0 {
		s += fmt.Sprintf(" %d/%d", b.Step, b.Steps)
	}
	if b.Elapsed != "" {
		s += " · " + b.Elapsed
	}
	return s
}

// ClaudePaneBackgroundWork returns every background-work item the
// (ANSI-stripped, untrimmed) Claude pane shows in flight, most specific
// first: workflow rows, then the awaited-agent / workflow line, then the
// footer counter. Finished workflow rows (n == m) are returned too, flagged
// by Step >= Steps, so the caller can tell a stale row apart; use
// ParseClaudeBackgroundWork for the single in-flight verdict.
func ClaudePaneBackgroundWork(content string) []BackgroundWork {
	if content == "" {
		return nil
	}
	var items []BackgroundWork
	for _, line := range lastNLines(content, backgroundRowScanLines) {
		if w, ok := parseClaudeWorkflowRow(line); ok {
			items = append(items, w)
		}
	}
	if w, ok := claudeAwaitedLine(content); ok {
		items = append(items, w)
	}
	if w, ok := claudeFooterCounter(content); ok {
		items = append(items, w)
	}
	return items
}

// ParseClaudeBackgroundWork returns the first in-flight background-work item
// the pane shows, or the zero value. Pure; Claude-shaped (callers gate it to
// Claude sessions).
func ParseClaudeBackgroundWork(content string) BackgroundWork {
	for _, w := range ClaudePaneBackgroundWork(content) {
		if w.Kind == BackgroundKindWorkflow && w.Steps > 0 && w.Step >= w.Steps {
			continue // finished row lingering under the footer
		}
		return w
	}
	return BackgroundWork{}
}

// parseClaudeWorkflowRow parses one workflow progress row.
func parseClaudeWorkflowRow(line string) (BackgroundWork, bool) {
	m := claudeWorkflowRowRe.FindStringSubmatch(strings.TrimSpace(StripANSI(line)))
	if m == nil {
		return BackgroundWork{}, false
	}
	step, err1 := strconv.Atoi(m[2])
	steps, err2 := strconv.Atoi(m[3])
	if err1 != nil || err2 != nil || steps <= 0 {
		return BackgroundWork{}, false
	}
	return BackgroundWork{
		Kind:    BackgroundKindWorkflow,
		Task:    m[1],
		Step:    step,
		Steps:   steps,
		Elapsed: strings.ReplaceAll(m[4], " ", ""),
		Source:  "pane",
	}, true
}

// claudeAwaitedLine reports a "✻ Waiting for N background agents / dynamic
// workflows to finish" line that is the CURRENT state of the turn: the last
// turn line above the input box (agent-roster rows drawn there by older
// builds are skipped). Without an input box in the frame the plain tail scan
// is used.
func claudeAwaitedLine(content string) (BackgroundWork, bool) {
	lines := strings.Split(content, "\n")
	prompt := -1
	// The input box sits above the footer and any agent-roster rows, so the
	// search reaches past a long roster (the corpus has 16, the audit 30).
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-backgroundPromptScanLines; i-- {
		if isPrompt, _ := isClaudePromptLine(strings.TrimSpace(StripANSI(lines[i]))); isPrompt {
			prompt = i
			break
		}
	}
	if prompt < 0 {
		recent := strings.Join(lastNLines(content, backgroundWorkScanLines), "\n")
		if m := claudeAwaitedAgentRe.FindStringSubmatch(recent); m != nil {
			return awaitedWork(m), true
		}
		return BackgroundWork{}, false
	}
	checked := 0
	for i := prompt - 1; i >= 0 && checked < backgroundWorkScanLines; i-- {
		line := strings.TrimSpace(StripANSI(lines[i]))
		if line == "" || isBoxBorder(line) {
			continue
		}
		checked++
		if line == "⏺ main" || strings.HasPrefix(line, "◯ ") || strings.HasPrefix(line, "● ") {
			continue // agent roster drawn above the box (older builds)
		}
		if m := claudeAwaitedAgentRe.FindStringSubmatch(line); m != nil {
			return awaitedWork(m), true
		}
		// Any other turn line (assistant output, a completion summary, a
		// previous prompt) means the turn moved on past any Waiting line.
		return BackgroundWork{}, false
	}
	return BackgroundWork{}, false
}

func awaitedWork(m []string) BackgroundWork {
	kind := BackgroundKindAgent
	if strings.Contains(strings.ToLower(m[2]), "workflow") {
		kind = BackgroundKindWorkflow
	}
	return BackgroundWork{Kind: kind, Task: m[1] + " " + strings.ToLower(strings.Join(strings.Fields(m[2]), " ")), Source: "pane"}
}

// isBoxBorder reports a horizontal rule of Claude's input box, including a
// labelled one ("──── work ─").
func isBoxBorder(line string) bool {
	return strings.HasPrefix(line, "──") || strings.Trim(line, "─━-") == ""
}

// claudeFooterCounter reads the live shell / monitor counter on the footer.
func claudeFooterCounter(content string) (BackgroundWork, bool) {
	for _, line := range lastNLines(content, backgroundWorkScanLines) {
		line = StripANSI(line)
		if !claudeFooterLineRe.MatchString(line) {
			continue
		}
		m := claudeFooterCounterRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		shells := 0
		for _, part := range claudeCounterPartRe.FindAllStringSubmatch(m[1], -1) {
			if n, err := strconv.Atoi(part[1]); err == nil && strings.HasPrefix(strings.ToLower(part[2]), "shell") {
				shells += n
			}
		}
		kind := BackgroundKindMonitor
		if shells > 0 {
			kind = BackgroundKindBash
		}
		return BackgroundWork{Kind: kind, Task: strings.TrimSpace(m[1]), Source: "pane"}, true
	}
	return BackgroundWork{}, false
}

// claudeBackgroundWorkPending reports whether the (ANSI-stripped) Claude pane
// shows background work in flight: a workflow row short of its last step, a
// turn awaiting background agents / workflows, or live shells / monitors in
// the footer. Pure and Claude-shaped; callers gate it to Claude sessions.
func claudeBackgroundWorkPending(content string) bool {
	return ParseClaudeBackgroundWork(content).InFlight()
}
