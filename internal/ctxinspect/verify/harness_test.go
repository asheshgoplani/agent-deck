package verify

import (
	"strings"
	"testing"
)

func TestParseAmount(t *testing.T) {
	tests := []struct {
		name       string
		digits     string
		suffix     string
		wantTokens int
		wantSlack  int
		wantOK     bool
	}{
		{name: "plain integer is exact", digits: "22813", wantTokens: 22813, wantSlack: 0, wantOK: true},
		{name: "grouped integer is exact", digits: "22,813", wantTokens: 22813, wantSlack: 0, wantOK: true},
		{name: "one decimal k carries 50 slack", digits: "2.3", suffix: "k", wantTokens: 2300, wantSlack: 50, wantOK: true},
		{name: "bare k carries 500 slack", digits: "93", suffix: "k", wantTokens: 93000, wantSlack: 500, wantOK: true},
		{name: "uppercase K is the same", digits: "93", suffix: "K", wantTokens: 93000, wantSlack: 500, wantOK: true},
		{name: "one decimal M carries 50k slack", digits: "1.2", suffix: "M", wantTokens: 1200000, wantSlack: 50000, wantOK: true},
		{name: "zero is a real figure", digits: "0", wantTokens: 0, wantSlack: 0, wantOK: true},
		{name: "bare decimal is not a token count", digits: "1.5", wantOK: false},
		{name: "non-numeric is rejected", digits: "x", wantOK: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tokens, slack, ok := parseAmount(tc.digits, tc.suffix)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if tokens != tc.wantTokens {
				t.Errorf("tokens = %d, want %d", tokens, tc.wantTokens)
			}
			if slack != tc.wantSlack {
				t.Errorf("slack = %d, want %d", slack, tc.wantSlack)
			}
		})
	}
}

// TestParseAmountSlackTracksPrintedPrecision is the reason slack is derived
// rather than assumed: "93k" and "93.0k" are the same number printed to ten
// times different accuracy, and a fixed slack would either hide a real
// disagreement or invent one.
func TestParseAmountSlackTracksPrintedPrecision(t *testing.T) {
	coarse, coarseSlack, _ := parseAmount("93", "k")
	fine, fineSlack, _ := parseAmount("93.0", "k")
	if coarse != fine {
		t.Fatalf("93k = %d and 93.0k = %d: same value expected", coarse, fine)
	}
	if coarseSlack <= fineSlack {
		t.Fatalf("coarse slack %d must exceed fine slack %d", coarseSlack, fineSlack)
	}
}

func TestNormalizeLabel(t *testing.T) {
	tests := map[string]string{
		"System prompt":       "system prompt",
		"  Memory  Files  ":   "memory files",
		"⛁ Custom agents":     "custom agents",
		"• Input":             "input",
		"Autocompact buffer ": "autocompact buffer",
	}
	for in, want := range tests {
		if got := normalizeLabel(in); got != want {
			t.Errorf("normalizeLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStripSGRRemovesColourWithoutTouchingText(t *testing.T) {
	in := "\x1b[1;32m⛁ System prompt:\x1b[0m \x1b[33m3.2k\x1b[0m tokens (1.6%)"
	out := stripSGR(in)
	if strings.Contains(out, "\x1b") {
		t.Fatalf("escape survived: %q", out)
	}
	if !strings.Contains(out, "System prompt: 3.2k tokens (1.6%)") {
		t.Fatalf("text was damaged: %q", out)
	}
}

// TestParseFiguresIgnoresIdentifiersAndPaths guards the one way a tolerant
// parser turns into a wrong number: reading the leading digits of an
// identifier as a token count.
func TestParseFiguresIgnoresIdentifiersAndPaths(t *testing.T) {
	pane := strings.Join([]string{
		"  • Path: /Users/example/project",
		"  • Session ID: 019fa876-1234-7890-abcd-000000000000",
		"  • Model: gpt-5.1-codex",
		"  • Input: 21,579",
	}, "\n")

	figures, unrecognized := parseFigures(pane)
	if len(figures) != 1 {
		t.Fatalf("figures = %v, want exactly the input row", figures)
	}
	if figures[0].Label != "input" || figures[0].Tokens != 21579 {
		t.Fatalf("figure = %+v, want input=21579", figures[0])
	}
	for _, u := range unrecognized {
		if strings.Contains(u, "Session ID") || strings.Contains(u, "Path") {
			t.Errorf("an identifier was reported as an unparsed figure: %q", u)
		}
	}
}

// TestParseFiguresReportsShapedLinesItCannotRead is the drift alarm: a row that
// looks like a figure and does not parse must be surfaced, because a row that
// silently stops parsing narrows the comparison instead of failing it.
func TestParseFiguresReportsShapedLinesItCannotRead(t *testing.T) {
	pane := "  ⛁ System prompt: 3.2k tokens (1.6%)\n  ⛁ Memory files: unknown tokens\n  ⛁ Skills: 1.5 tokens"

	figures, unrecognized := parseFigures(pane)
	if len(figures) != 1 {
		t.Fatalf("figures = %v, want only the parseable row", figures)
	}
	joined := strings.Join(unrecognized, "|")
	if !strings.Contains(joined, "Skills") {
		t.Errorf("a shaped-but-unparseable row must be reported, got %q", joined)
	}
}

func TestParseFiguresKeepsFirstOccurrenceOfARepeatedLabel(t *testing.T) {
	pane := "⛁ Memory files: 4.5k tokens\n...redraw...\n⛁ Memory files: 4.5k tokens"
	figures, _ := parseFigures(pane)
	if len(figures) != 1 {
		t.Fatalf("a redrawn panel must not double-count: %v", figures)
	}
}

func TestParseWindowSummary(t *testing.T) {
	used, slack, window, ok := parseWindowSummary("claude-opus-4-7 · 116k/200k tokens (58%)")
	if !ok {
		t.Fatal("the header summary must parse")
	}
	if used != 116000 || window != 200000 {
		t.Fatalf("used=%d window=%d, want 116000/200000", used, window)
	}
	if slack != 500 {
		t.Fatalf("slack = %d, want 500 for a bare-k figure", slack)
	}
}

func TestParsePercentLeft(t *testing.T) {
	for _, in := range []string{"78% context left", "Context left: 78%", "  78 % of context left  "} {
		pct, ok := parsePercentLeft(in)
		if !ok {
			t.Fatalf("%q must parse", in)
		}
		if pct != 78 {
			t.Fatalf("%q → %v, want 78", in, pct)
		}
	}
	if _, ok := parsePercentLeft("nothing here"); ok {
		t.Fatal("an unrelated line must not yield a percentage")
	}
}

func TestHarnessReportFigureLookup(t *testing.T) {
	h := &HarnessReport{Figures: []Figure{{Label: "memory files", Tokens: 4500}}}
	if f, ok := h.Figure("memory files"); !ok || f.Tokens != 4500 {
		t.Fatalf("lookup failed: %+v ok=%v", f, ok)
	}
	if _, ok := h.Figure("skills"); ok {
		t.Fatal("an absent label must not be found")
	}
	var nilReport *HarnessReport
	if _, ok := nilReport.Figure("anything"); ok {
		t.Fatal("a nil report must answer no")
	}
}
