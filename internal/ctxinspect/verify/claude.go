package verify

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
	"github.com/asheshgoplani/agent-deck/internal/ctxinspect/claude"
)

// Claude Code's /context panel labels, normalized. They are listed explicitly
// rather than accepted by shape so that a renamed or newly added row shows up
// as unmapped in the parity table instead of quietly joining a group it does
// not belong to.
const (
	claudeLabelSystemPrompt = "system prompt"
	claudeLabelSystemTools  = "system tools"
	claudeLabelMCPTools     = "mcp tools"
	claudeLabelMemoryFiles  = "memory files"
	claudeLabelCustomAgents = "custom agents"
	claudeLabelAgents       = "agents"
	claudeLabelSkills       = "skills"
	claudeLabelCustomSkills = "custom skills"
	claudeLabelMessages     = "messages"
	claudeLabelFreeSpace    = "free space"
	claudeLabelAutocompact  = "autocompact buffer"
	claudeLabelToolResults  = "tool responses"
)

// claudeContributorLabels are the rows that occupy the context window. Free
// space and the autocompact buffer are printed by the same panel but are not
// contributions, so they are excluded from every sum.
var claudeContributorLabels = []string{
	claudeLabelSystemPrompt,
	claudeLabelSystemTools,
	claudeLabelMCPTools,
	claudeLabelMemoryFiles,
	claudeLabelCustomAgents,
	claudeLabelAgents,
	claudeLabelSkills,
	claudeLabelCustomSkills,
	claudeLabelMessages,
	claudeLabelToolResults,
}

// claudeNonContributorLabels are printed by the panel but describe what is
// *not* in the window.
var claudeNonContributorLabels = []string{
	claudeLabelFreeSpace,
	claudeLabelAutocompact,
}

// claudeModelLine extracts the model identifier from the panel's header, which
// prints it immediately before the "used/window tokens" summary.
var claudeModelLine = regexp.MustCompile(`([A-Za-z][A-Za-z0-9.@_-]{2,})\s*[·•|]\s*[0-9]`)

// minClaudeFigures is how many recognized contributor rows a pane must carry
// before it is accepted as a /context panel. Two is enough to distinguish the
// panel from an ordinary chat line that happens to mention a token count, and
// low enough that a version which drops a row still verifies.
const minClaudeFigures = 2

// ParseClaudeContext reads Claude Code's /context panel off a captured pane.
//
// It returns [ErrNoAccounting] when the pane holds no recognizable panel — the
// command is unavailable in this version, the panel has not rendered yet, or
// the format changed. It never returns a report with fabricated zeroes: an
// absent row is absent, and the parity table says the harness was silent about
// it rather than that the harness reported nothing there.
func ParseClaudeContext(pane string) (*HarnessReport, error) {
	figures, unrecognized := parseFigures(pane)

	known := make(map[string]bool, len(claudeContributorLabels)+len(claudeNonContributorLabels))
	for _, l := range claudeContributorLabels {
		known[l] = true
	}
	for _, l := range claudeNonContributorLabels {
		known[l] = true
	}

	kept := make([]Figure, 0, len(figures))
	recognized := 0
	for _, f := range figures {
		if !known[f.Label] {
			// Not a panel row: a chat line, a status bar, an unrelated
			// "label: number". Keep it visible rather than silently dropping it.
			unrecognized = append(unrecognized, fmt.Sprintf("%s: %d (label not part of the /context panel)", f.Raw, f.Tokens))
			continue
		}
		kept = append(kept, f)
		if isContributor(f.Label, claudeContributorLabels) {
			recognized++
		}
	}

	if recognized < minClaudeFigures {
		return nil, fmt.Errorf("%w: found %d recognizable /context rows (need %d); parsed: %s",
			ErrNoAccounting, recognized, minClaudeFigures, describeFigures(kept))
	}

	rep := &HarnessReport{
		Harness:      "claude",
		Command:      ClaudeCommand,
		Figures:      kept,
		Unrecognized: unrecognized,
	}
	if used, slack, window, ok := parseWindowSummary(pane); ok {
		rep.Used, rep.UsedSlack, rep.Window = used, slack, window
	}
	rep.Model = claudePanelModel(pane)
	return rep, nil
}

// claudePanelModel extracts the model identifier the panel header names.
func claudePanelModel(pane string) string {
	for _, line := range strings.Split(stripSGR(pane), "\n") {
		if !windowLine.MatchString(line) {
			continue
		}
		if m := claudeModelLine.FindStringSubmatch(line); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

// isContributor reports whether label is in the given contributor set.
func isContributor(label string, set []string) bool {
	for _, l := range set {
		if l == label {
			return true
		}
	}
	return false
}

// ClaudeCommand is the slash command that renders Claude Code's own context
// accounting.
const ClaudeCommand = "/context"

// claudeReady is the poll predicate used while waiting for the panel to render.
// It is the parser itself rather than a marker string: "the panel is ready"
// and "the panel can be read" are the same question, and asking it twice with
// two different answers is how a half-drawn panel gets parsed.
func claudeReady(pane string) bool {
	_, err := ParseClaudeContext(pane)
	return err == nil
}

// claudeGroups are the comparable quantities between Claude's panel and an
// agent-deck report.
//
// The mapping is deliberately coarse where the two accountings genuinely differ.
// Claude's "System tools" row prices whole tool schemas; agent-deck's
// system-tools category prices only the deferred tool *names*, because the
// schemas are never written to disk and are therefore inside the residual.
// Diffing those two rows one-to-one would report a large, permanent, meaningless
// disagreement. Grouped with the residual, the arithmetic is equivalent and the
// comparison means something.
func claudeGroups() []Group {
	return []Group{
		{
			Name:          "memory files",
			HarnessLabels: []string{claudeLabelMemoryFiles},
			Categories:    []string{claude.CategoryMemory},
			Note:          "the CLAUDE.md hierarchy; agent-deck rebuilds it from disk, Claude reports what it injected",
		},
		{
			Name:          "agents",
			HarnessLabels: []string{claudeLabelCustomAgents, claudeLabelAgents},
			Categories:    []string{claude.CategoryAgents},
			Note:          "the startup agent catalogue",
		},
		{
			Name:          "skills",
			HarnessLabels: []string{claudeLabelSkills, claudeLabelCustomSkills},
			Categories:    []string{claude.CategorySkills},
			Note:          "the startup skill catalogue; skills merely present on disk cost nothing and are excluded on both sides",
		},
		{
			Name:            "harness internals",
			HarnessLabels:   []string{claudeLabelSystemPrompt, claudeLabelSystemTools, claudeLabelMCPTools},
			Categories:      []string{claude.CategorySystemTools, ctxinspect.CategoryMCP},
			IncludeResidual: true,
			Note:            "compared as a group: agent-deck prices tool names and MCP instruction blocks and leaves the schemas in the residual, so only the sum is equivalent",
		},
		{
			Name:           "messages",
			HarnessLabels:  []string{claudeLabelMessages, claudeLabelToolResults},
			IncludeHistory: true,
			Informational:  true,
			Note:           "conversation history; agent-deck reports one orientation figure and does not attribute it, so this row is informational",
		},
		{
			Name:            "total occupancy",
			HarnessLabels:   claudeContributorLabels,
			Categories:      allClaudeCategories(),
			IncludeResidual: true,
			IncludeHistory:  true,
			Note:            "every contributing row against the whole report; free space and the autocompact buffer are excluded because they are not in the window",
		},
	}
}

// allClaudeCategories lists every category the Claude adapter can emit, so the
// total row cannot silently omit one that a future adapter version adds.
func allClaudeCategories() []string {
	return []string{
		claude.CategorySystemTools,
		ctxinspect.CategoryMCP,
		claude.CategoryMemory,
		claude.CategorySkills,
		claude.CategoryAgents,
	}
}
