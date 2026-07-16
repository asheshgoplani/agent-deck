package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Conductor turn-start context (the "fleet snapshot" hook). At the Claude
// turn-start edges — UserPromptSubmit and SessionStart — a parent session gets
// a compact snapshot of its children injected as additionalContext, so a
// conductor always sees CURRENT fleet state without running `session children`
// itself. This complements the issue #1225 Stop-edge drain, which delivers
// queued transition/completion EVENTS: the drain is deltas, this is state —
// it survives conductor restarts and events consumed by other drain paths.
//
// Silent no-op for sessions with no children (every leaf session), so the
// injection costs non-parents one storage read per user prompt and adds zero
// context. Opt out per session with AGENTDECK_NO_CHILDREN_CONTEXT=1.

// maxContextBullets caps each bullet list (waiting / completed) so a huge
// fleet cannot flood the conductor's context window.
const maxContextBullets = 5

// claudeContextEventName maps a hook event to the exact Claude-cased name
// required in hookSpecificOutput, or "" for events (or non-Claude agents)
// where context injection does not apply.
func claudeContextEventName(event string) string {
	switch normalizeHookEventKey(event) {
	case "userpromptsubmit":
		return "UserPromptSubmit"
	case "sessionstart":
		return "SessionStart"
	default:
		return ""
	}
}

// childrenContextJSON renders the Claude Code hook JSON that injects context
// without touching the prompt or blocking the turn.
func childrenContextJSON(hookEventName, context string) string {
	payload := map[string]map[string]string{
		"hookSpecificOutput": {
			"hookEventName":     hookEventName,
			"additionalContext": context,
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(b)
}

// formatChildrenContext renders the fleet snapshot. Header always carries the
// counts; bullets list only the ACTIONABLE children — waiting (stalled on a
// question) and completed (result ready to collect) — each with the exact
// command to run next. Running children are counted but not listed.
func formatChildrenContext(rows []childRow) string {
	if len(rows) == 0 {
		return ""
	}

	var waiting, done []childRow
	running := 0
	for _, r := range rows {
		switch {
		case r.DoneStatus != "":
			done = append(done, r)
		case r.Status == "waiting":
			waiting = append(waiting, r)
		case r.Status == "running":
			running++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[agent-deck fleet] %d children: %d running, %d waiting, %d done",
		len(rows), running, len(waiting), len(done))
	// Children in none of the three buckets (stopped, error, idle without a
	// sentinel) — surfaced so the header math always adds up.
	if other := len(rows) - running - len(waiting) - len(done); other > 0 {
		fmt.Fprintf(&b, ", %d stopped/other", other)
	}

	for i, r := range waiting {
		if i == maxContextBullets {
			fmt.Fprintf(&b, "\n- …and %d more waiting", len(waiting)-maxContextBullets)
			break
		}
		// Title names the child for the reader; the commands target its ID. A
		// title is a display string — it can contain spaces, or repeat across
		// sessions — so pasting one as an argument is what breaks the command.
		fmt.Fprintf(&b, "\n- waiting on input: %s — see `agent-deck session output %s --json`, answer with `agent-deck session send %s \"...\"`",
			r.Title, r.ID, r.ID)
	}
	for i, r := range done {
		if i == maxContextBullets {
			fmt.Fprintf(&b, "\n- …and %d more completed", len(done)-maxContextBullets)
			break
		}
		line := fmt.Sprintf("\n- completed: %s → %s", r.Title, r.DoneStatus)
		if r.DoneSummary != "" {
			line += " — " + r.DoneSummary
		}
		line += fmt.Sprintf(" (collect: `agent-deck session output %s --json`)", r.ID)
		b.WriteString(line)
	}

	b.WriteString("\nFull list: `agent-deck session children --json`")
	return b.String()
}

// buildChildrenContextSummary loads the caller's children and renders the
// snapshot, or "" when the session has none (or anything fails — the hook
// must never break a turn over supervision sugar).
func buildChildrenContextSummary(instanceID string) string {
	profile := session.GetEffectiveProfile("")
	storage, instances, _, err := loadSessionData(profile)
	if err != nil {
		return ""
	}
	defer func() { _ = storage.Close() }()

	kids := childrenOf(instanceID, instances)
	if len(kids) == 0 {
		return ""
	}
	session.RefreshInstancesForCLIStatus(kids)
	return formatChildrenContext(buildChildRows(kids))
}
