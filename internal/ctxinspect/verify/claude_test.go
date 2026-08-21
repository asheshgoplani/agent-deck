package verify

import (
	"errors"
	"strings"
	"testing"
)

// claudeContextPane is a Claude Code /context panel as it is drawn: a decorative
// occupancy grid on the left, one labelled row per contributor on the right,
// abbreviated figures, and two rows that describe what is *not* in the window.
const claudeContextPane = `
> /context

  Context Usage
  claude-opus-4-7 · 116k/200k tokens (58%)

  ⛁ ⛁ ⛁ ⛁ ⛁ ⛁ ⛁ ⛁ ⛁ ⛁     ⛁ System prompt: 3.2k tokens (1.6%)
  ⛁ ⛁ ⛁ ⛁ ⛁ ⛁ ⛁ ⛁ ⛁ ⛁     ⛁ System tools: 12.4k tokens (6.2%)
  ⛁ ⛁ ⛁ ⛁ ⛁ ⛁ ⛁ ⛁ ⛁ ⛁     ⛁ MCP tools: 1.5k tokens (0.8%)
  ⛁ ⛁ ⛁ ⛁ ⛁ ⛁ ⛁ ⛁ ⛁ ⛁     ⛁ Memory files: 4.5k tokens (2.3%)
  ⛁ ⛁ ⛁ ⛁ ⛁ ⛁ ⛁ ⛶ ⛶ ⛶     ⛁ Custom agents: 1.6k tokens (0.8%)
  ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶     ⛁ Messages: 93.0k tokens (46.5%)
  ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶     ⛶ Free space: 84k (42%)
                            ⛝ Autocompact buffer: 45.0k tokens (22.5%)
`

func TestParseClaudeContext(t *testing.T) {
	h, err := ParseClaudeContext(claudeContextPane)
	if err != nil {
		t.Fatalf("ParseClaudeContext: %v", err)
	}
	if h.Harness != "claude" || h.Command != ClaudeCommand {
		t.Fatalf("identity = %s/%s, want claude//context", h.Harness, h.Command)
	}
	if h.Model != "claude-opus-4-7" {
		t.Errorf("Model = %q, want claude-opus-4-7", h.Model)
	}
	if h.Window != 200000 || h.Used != 116000 {
		t.Errorf("window/used = %d/%d, want 200000/116000", h.Window, h.Used)
	}

	want := map[string]int{
		"system prompt":      3200,
		"system tools":       12400,
		"mcp tools":          1500,
		"memory files":       4500,
		"custom agents":      1600,
		"messages":           93000,
		"free space":         84000,
		"autocompact buffer": 45000,
	}
	for label, tokens := range want {
		f, ok := h.Figure(label)
		if !ok {
			t.Errorf("missing row %q", label)
			continue
		}
		if f.Tokens != tokens {
			t.Errorf("%s = %d, want %d", label, f.Tokens, tokens)
		}
	}
	if len(h.Figures) != len(want) {
		t.Errorf("parsed %d rows, want %d: %s", len(h.Figures), len(want), describeFigures(h.Figures))
	}
}

// TestParseClaudeContextRejectsAPaneWithoutThePanel is the honesty gate: a pane
// that is not the panel must be an error, not an empty report. An empty report
// would render as a table of confident zeroes.
func TestParseClaudeContextRejectsAPaneWithoutThePanel(t *testing.T) {
	for _, pane := range []string{
		"",
		"⏺ I've finished the refactor. Total: 4 files changed.",
		"Unknown slash command: /context",
	} {
		h, err := ParseClaudeContext(pane)
		if !errors.Is(err, ErrNoAccounting) {
			t.Fatalf("pane %q: err = %v, want ErrNoAccounting", pane, err)
		}
		if h != nil {
			t.Fatalf("pane %q: a failed parse must not return a report", pane)
		}
	}
}

// TestParseClaudeContextSurfacesUnknownRows covers the version-drift case: a
// renamed or newly added row must appear in Unrecognized so the operator can
// see the format moved, instead of vanishing from every sum.
func TestParseClaudeContextSurfacesUnknownRows(t *testing.T) {
	pane := claudeContextPane + "\n  ⛁ Background tasks: 2.0k tokens (1.0%)\n"
	h, err := ParseClaudeContext(pane)
	if err != nil {
		t.Fatalf("ParseClaudeContext: %v", err)
	}
	if _, ok := h.Figure("background tasks"); ok {
		t.Fatal("an unknown row must not be accepted as a panel row")
	}
	joined := strings.Join(h.Unrecognized, "|")
	if !strings.Contains(joined, "Background tasks") {
		t.Fatalf("an unknown row must be surfaced, got %q", joined)
	}
}

func TestClaudeReadyIsTheParser(t *testing.T) {
	if claudeReady("half-drawn panel\n⛁ System prompt: 3.2k tokens") {
		t.Error("one row is not a rendered panel")
	}
	if !claudeReady(claudeContextPane) {
		t.Error("a complete panel must be ready")
	}
}

func TestClaudeGroupsCoverEveryContributorLabel(t *testing.T) {
	claimed := make(map[string]bool)
	for _, g := range claudeGroups() {
		for _, l := range g.HarnessLabels {
			claimed[l] = true
		}
	}
	for _, l := range claudeContributorLabels {
		if !claimed[l] {
			t.Errorf("contributor label %q is claimed by no group, so it would never be compared", l)
		}
	}
}

// TestClaudeGroupsNeverPairSystemToolsAlone documents the deliberate coarseness:
// Claude prices whole tool schemas under "System tools" while agent-deck prices
// only the deferred tool names, so a one-to-one row would report a large,
// permanent, meaningless disagreement.
func TestClaudeGroupsNeverPairSystemToolsAlone(t *testing.T) {
	for _, g := range claudeGroups() {
		if len(g.HarnessLabels) == 1 && g.HarnessLabels[0] == claudeLabelSystemTools {
			t.Fatalf("group %q diffs System tools on its own", g.Name)
		}
		if isContributor(claudeLabelSystemTools, g.HarnessLabels) && !g.IncludeResidual {
			t.Fatalf("group %q includes System tools without the residual, so the two sides cannot be equivalent", g.Name)
		}
	}
}
