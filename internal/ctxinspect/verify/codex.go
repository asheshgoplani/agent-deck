package verify

import (
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect/codex"
)

// Codex's /status panel labels, normalized.
const (
	codexLabelInput         = "input"
	codexLabelOutput        = "output"
	codexLabelTotal         = "total"
	codexLabelCached        = "cached"
	codexLabelReasoning     = "reasoning"
	codexLabelContextWindow = "context window"
	codexLabelContextUsed   = "context used"
	codexLabelContextLeft   = "context left"
	codexLabelTokenUsage    = "token usage"
)

// codexKnownLabels are the rows /status prints that carry a token figure.
var codexKnownLabels = []string{
	codexLabelInput,
	codexLabelOutput,
	codexLabelTotal,
	codexLabelCached,
	codexLabelReasoning,
	codexLabelContextWindow,
	codexLabelContextUsed,
	codexLabelContextLeft,
	codexLabelTokenUsage,
}

// codexModelLine matches the "Name: <model>" row inside /status's model
// section. The value is an identifier, not a number, so it never reaches
// [parseFigures].
var codexModelLine = regexp.MustCompile(`(?i)^\W*(?:name|model)\s*:\s*([A-Za-z][A-Za-z0-9./@_-]{2,})\s*$`)

// CodexCommand is the slash command that renders Codex's own accounting.
const CodexCommand = "/status"

// ParseCodexStatus reads Codex CLI's /status panel off a captured pane.
//
// Codex's panel is structurally different from Claude's: it reports *cumulative
// session* token usage (input / output / total across every turn) plus a
// context-window occupancy, and it prints no per-category breakdown at all.
// Only the occupancy figure is comparable with an agent-deck report, and the
// package says so rather than inventing rows — the cumulative totals are a
// different quantity, and diffing them against a fixed prefix would be a
// category error dressed up as a number.
func ParseCodexStatus(pane string) (*HarnessReport, error) {
	figures, unrecognized := parseFigures(pane)

	known := make(map[string]bool, len(codexKnownLabels))
	for _, l := range codexKnownLabels {
		known[l] = true
	}

	kept := make([]Figure, 0, len(figures))
	for _, f := range figures {
		if !known[f.Label] {
			unrecognized = append(unrecognized, fmt.Sprintf("%s: %d (label not part of the /status panel)", f.Raw, f.Tokens))
			continue
		}
		kept = append(kept, f)
	}
	if len(kept) == 0 {
		return nil, fmt.Errorf("%w: no /status token rows found; parsed: %s", ErrNoAccounting, describeFigures(figures))
	}

	rep := &HarnessReport{
		Harness:      "codex",
		Command:      CodexCommand,
		Figures:      kept,
		Unrecognized: unrecognized,
		Model:        codexPanelModel(pane),
	}

	if f, ok := rep.Figure(codexLabelContextWindow); ok {
		rep.Window = f.Tokens
	}
	if used, slack, window, ok := parseWindowSummary(pane); ok {
		rep.Used, rep.UsedSlack = used, slack
		if rep.Window == 0 {
			rep.Window = window
		}
	}
	if rep.Used == 0 {
		if f, ok := rep.Figure(codexLabelContextUsed); ok {
			rep.Used, rep.UsedSlack = f.Tokens, f.Slack
		}
	}
	if rep.Used == 0 && rep.Window > 0 {
		if pct, ok := parsePercentLeft(pane); ok {
			rep.Used = int(math.Round(float64(rep.Window) * (100 - pct) / 100))
			// A whole printed percent of the window is the granularity of the
			// figure, so half of it is the slack.
			rep.UsedSlack = int(math.Round(float64(rep.Window) / 200))
			rep.UsedDerived = true
		}
	}
	return rep, nil
}

// codexPanelModel extracts the model identifier /status names.
func codexPanelModel(pane string) string {
	for _, line := range strings.Split(stripSGR(pane), "\n") {
		if m := codexModelLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

// minCodexFigures is how many /status rows a pane must carry before the panel
// is considered rendered.
const minCodexFigures = 2

// codexReady is the poll predicate used while waiting for /status to render.
func codexReady(pane string) bool {
	rep, err := ParseCodexStatus(pane)
	return err == nil && len(rep.Figures) >= minCodexFigures
}

// codexGroups are the comparable quantities between Codex's /status panel and
// an agent-deck report.
//
// There is exactly one, and it is the honest answer: /status publishes an
// occupancy and nothing else that means the same thing as a fixed prefix. The
// cumulative input/output/total rows are listed by the parser and printed in
// the output, but they are not diffed against anything, because there is
// nothing on the agent-deck side that is the same quantity.
func codexGroups() []Group {
	return []Group{
		{
			Name:            "total occupancy",
			HarnessUsed:     true,
			Categories:      allCodexCategories(),
			IncludeResidual: true,
			IncludeHistory:  true,
			Note:            "Codex reports one occupancy figure and no per-category breakdown; the cumulative input/output/total rows are a different quantity and are not diffed",
		},
	}
}

// allCodexCategories lists every category the Codex adapter can emit.
func allCodexCategories() []string {
	return []string{
		codex.CategoryBaseInstructions,
		codex.CategoryAgentsMD,
		codex.CategoryHostSkills,
		codex.CategoryEnvironments,
		codex.CategoryDeveloper,
		codex.CategoryInjected,
	}
}
