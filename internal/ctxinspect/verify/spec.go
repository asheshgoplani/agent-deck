package verify

import (
	"fmt"
	"strings"
)

// Spec is everything needed to compare one harness against an agent-deck
// report: the command that renders its own accounting, how to read that
// accounting back, and which quantities are genuinely comparable.
//
// A harness with no Spec is not verifiable, and [SpecForAdapter] says so
// explicitly rather than returning an empty Spec that would silently produce an
// all-green table.
type Spec struct {
	// Harness is the adapter name this spec verifies, e.g. "claude".
	Harness string
	// Command is the slash command that renders the harness's own accounting.
	// Sending it mutates the user's live session.
	Command string
	// Parse reads a captured pane into a [HarnessReport].
	Parse func(pane string) (*HarnessReport, error)
	// Ready reports whether a captured pane already holds a readable panel. It
	// is the poll predicate: it is the parser, not a marker string, so "the
	// panel has rendered" and "the panel can be read" cannot disagree.
	Ready func(pane string) bool
	// Groups are the comparable quantities, in table order.
	Groups []Group
	// ExcludedLabels are panel rows that carry a token figure but are not
	// contributions to the window (free space, the autocompact buffer). They
	// are parsed, printed, and deliberately not summed.
	ExcludedLabels []string
	// Note is a sentence about what this harness can and cannot be verified on.
	Note string
}

// SpecForAdapter returns the verification spec for an adapter name — the value
// of [ctxinspect.Report.Adapter], not the agent-deck tool name.
//
// Keying on the adapter is deliberate: the adapter already decided which
// harness family a tool belongs to (including every claude-compatible and
// codex-compatible wrapper), and re-deriving that here would be a second
// opinion that could disagree with the report being verified.
func SpecForAdapter(name string) (Spec, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "claude":
		return Spec{
			Harness:        "claude",
			Command:        ClaudeCommand,
			Parse:          ParseClaudeContext,
			Ready:          claudeReady,
			Groups:         claudeGroups(),
			ExcludedLabels: claudeNonContributorLabels,
			Note:           "Claude Code's /context prints a per-category breakdown, so most groups are directly comparable; the system-prompt and tool-schema rows are compared as a group with the residual because agent-deck cannot see their text.",
		}, true
	case "codex":
		return Spec{
			Harness: "codex",
			Command: CodexCommand,
			Parse:   ParseCodexStatus,
			Ready:   codexReady,
			Groups:  codexGroups(),
			// Every /status row is excluded from the unmapped list: the
			// occupancy rows are consumed through [HarnessReport.Used] rather
			// than by label, and the cumulative rows are a different quantity
			// that no group claims on purpose. Listing either as "unmapped"
			// would report the panel's normal output as a surprise.
			ExcludedLabels: []string{
				codexLabelInput, codexLabelOutput, codexLabelTotal, codexLabelCached,
				codexLabelReasoning, codexLabelContextWindow, codexLabelContextUsed,
				codexLabelContextLeft, codexLabelTokenUsage,
			},
			Note: "Codex's /status publishes a context occupancy and cumulative session usage, but no per-category breakdown, so only the occupancy total can be compared.",
		}, true
	}
	return Spec{}, false
}

// ErrUnverifiable is returned when a harness has no way to report its own
// accounting, so there is nothing to compare against.
//
// It is a distinct error from [ErrNoAccounting]: one means "this harness cannot
// be verified at all", the other means "this harness can be, and this capture
// did not contain the panel". Collapsing them would hide a format change behind
// a capability limit.
type ErrUnverifiable struct {
	// Adapter is the adapter name that has no spec.
	Adapter string
}

// Error implements error.
func (e ErrUnverifiable) Error() string {
	return fmt.Sprintf("ctxinspect/verify: no ground-truth command is known for the %q adapter, so its report cannot be checked against the harness's own accounting", e.Adapter)
}
