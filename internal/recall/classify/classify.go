// Package classify assigns the free (T0) message class at ingest and holds
// the rules the cheap (T1) classifiers in internal/recall/enrich run on.
// The taxonomy is skills/agent-deck/scripts/self-improvement/distill.py's
// (PROMPT / USER / TOOL / ERROR / ASSIST / HEARTBEAT / SKILL_LOAD), and
// since phase 4 both read it from one rules.json (rules.go), so the Go and
// Python paths cannot drift.
package classify

import "strings"

// Class is stored in msg.class.
type Class int

const (
	Unknown Class = iota
	// Prompt is text a human typed (or an agent sent as its turn).
	Prompt
	// Meta is user-role text the harness injected: system reminders,
	// slash-command envelopes, compaction summaries.
	Meta
	// Tool is a tool result (never indexed as a body; counted).
	Tool
	// Error is an errored tool result.
	Error
	// Assist is assistant text.
	Assist
	// Heartbeat is a [HEARTBEAT] or [EVENT] prompt from a conductor loop.
	Heartbeat
	// SkillLoad is the user-role message a skill expansion produces.
	SkillLoad
	// Interrupt is "[Request interrupted by user]".
	Interrupt
	// CompactSummary is the user-role continuation summary after compaction.
	CompactSummary
)

// Names indexes Class for CLI output.
var Names = [...]string{"unknown", "prompt", "meta", "tool", "error", "assist", "heartbeat", "skill_load", "interrupt", "compact_summary"}

func (c Class) String() string {
	if int(c) < len(Names) {
		return Names[c]
	}
	return "unknown"
}

// SkillLoadMarker is rules.json's skill_load_marker (distill.py's
// SKILL_LOAD_MARKER).
var SkillLoadMarker = rules.SkillLoadMarker

// Signals are the structural facts the reader observed about one record.
type Signals struct {
	Assistant      bool
	ToolResult     bool
	IsError        bool
	IsMeta         bool
	CompactSummary bool
}

// Message classifies one decoded message. Text is the decoded body (text
// blocks only); an empty body with ToolResult set is a bare tool result.
func Message(text string, s Signals) Class {
	switch {
	case s.Assistant:
		return Assist
	case s.ToolResult && s.IsError:
		return Error
	case s.ToolResult:
		return Tool
	case s.CompactSummary:
		return CompactSummary
	}
	t := strings.TrimSpace(text)
	switch {
	case IsInterrupt(t):
		return Interrupt
	case IsHeartbeat(t):
		return Heartbeat
	case strings.Contains(t, SkillLoadMarker):
		return SkillLoad
	case s.IsMeta || hasAnyPrefix(t, rules.MetaPrefixes):
		return Meta
	}
	return Prompt
}

func hasAnyPrefix(t string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// IsHeartbeat mirrors distill.py's is_heartbeat (rules.json
// heartbeat_prefixes).
func IsHeartbeat(text string) bool {
	return hasAnyPrefix(strings.TrimSpace(text), rules.HeartbeatPrefix)
}

// IsInterrupt reports the Escape-key marker (rules.json interrupt_marker).
func IsInterrupt(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), rules.InterruptMarker)
}

// SkillName mirrors distill.py's extract_skill_name: the last path segment
// on the marker's line, or "" when the text is not a skill load.
func SkillName(text string) string {
	_, after, ok := strings.Cut(text, SkillLoadMarker)
	if !ok {
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(after), "\n")
	line = strings.TrimRight(strings.TrimSpace(line), "/")
	if line == "" {
		return "unknown"
	}
	if i := strings.LastIndexByte(line, '/'); i >= 0 {
		line = line[i+1:]
	}
	if line == "" {
		return "unknown"
	}
	return line
}

// Countable reports whether a class counts as a conversational turn
// (session.turns): a human prompt, not harness plumbing.
func Countable(c Class) bool {
	return c == Prompt
}
