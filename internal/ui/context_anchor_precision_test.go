package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
)

// The report labels exactly one figure MEASURED, and invites the reader to check
// it against the provider's own accounting. It was not checkable on any frame
// they could reach: the Verify table rounds it (27.0k matches 26,951 and 27,013
// equally well), the field path and component sums sat in a last table column
// the pane clips, and the caveat carrying the cache-read split lost its numbers
// to the same clip. A figure offered for verification that cannot be verified is
// the one claim this feature may not make.

// buildPreciseAnchorReport is a report whose figures are deliberately not round,
// so a rounded render cannot accidentally pass.
func buildPreciseAnchorReport() *ctxinspect.Report {
	cat := ctxinspect.Category{
		Name:  "memory-files",
		Title: "memory files",
		Items: []ctxinspect.Item{{
			ID:      "memory:global",
			Label:   "~/.claude/CLAUDE.md",
			Content: ctxinspect.ReconstructedContent(ctxinspect.KindProse, "rules\n", "rebuilt from disk"),
			Load:    ctxinspect.LoadedNowCost(ctxinspect.Estimated(6588, "chars/2.4")),
			Origin:  ctxinspect.OriginUserConfig,
			Lever:   ctxinspect.FileLever("/home/u/.claude/CLAUDE.md", "loaded for every session"),
		}},
	}
	rep := &ctxinspect.Report{
		Harness:     "claude",
		Adapter:     "claude",
		SessionID:   "abcd-1234",
		ProjectPath: "/repo",
		Model:       "claude-fable-5",
		Window:      ctxinspect.WindowInfo{Tokens: 1_000_000, Source: ctxinspect.WindowModelDefault, Detail: "claude-fable-5"},
		Basis:       ctxinspect.BasisObserved,
		Anchor: &ctxinspect.Anchor{
			Tokens: ctxinspect.Measured(27013, "message.usage.iterations[0]"),
			Source: "first assistant turn: message.usage.iterations[0].input_tokens + cache_creation_input_tokens + cache_read_input_tokens (input 1201 + cache creation 5400 + cache read 20412)",
		},
		Categories: []ctxinspect.Category{cat},
	}
	rep.AddCaveat("anchor-warm-cache",
		"20412 of the measured 27013 prompt tokens were served from Anthropic's prompt cache. The cache is shared between sessions, so this is normal for a fresh session and does not change the prompt's size.",
		ctxinspect.SeverityInfo)
	rep.Reconcile()
	return rep
}

// verifyTabText scrolls the Verify tab from top to bottom at one width and
// returns everything the reader could actually see.
func verifyTabText(t *testing.T, rep *ctxinspect.Report, width, height int) string {
	t.Helper()
	p := NewContextPager()
	p.Show("demo", "session-1", "claude", width, height)
	p.SetReport(rep, nil)
	p.SetTab(contextPagerTabVerify)

	var all strings.Builder
	p.Top()
	for {
		all.WriteString(ansi.Strip(p.View()))
		all.WriteString("\n")
		if p.current().offset >= p.maxOffset() {
			break
		}
		p.PageDown()
	}
	return all.String()
}

// TestVerifyTabStatesTheAnchorAtFullPrecision is the regression test.
func TestVerifyTabStatesTheAnchorAtFullPrecision(t *testing.T) {
	out := verifyTabText(t, buildPreciseAnchorReport(), 120, 30)

	if !strings.Contains(out, "27,013") {
		t.Errorf("the Verify tab never states the measured total to the digit, so it cannot be checked against the provider's accounting:\n%s", out)
	}
	// The field path and the component sums, including the cache-read share.
	for _, want := range []string{
		"iterations[0]",
		"cache read 20412",
		"27,013 measured",
		"6,588 attributed",
		"20,425 unattributed remainder",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the Verify tab is missing %q: the arithmetic behind the residual must be readable, not inferable\n%s", want, out)
		}
	}
}

// TestVerifyTabAnchorBlockIsWrappedNotClipped pins the mechanism. Adding the
// figures is worthless if they are added to a line the pane cuts, which is
// exactly what happened when they lived in the table's last column.
func TestVerifyTabAnchorBlockIsWrappedNotClipped(t *testing.T) {
	for _, size := range []struct {
		name          string
		width, height int
	}{{"wide", 160, 48}, {"normal", 120, 30}, {"small", 80, 24}} {
		t.Run(size.name, func(t *testing.T) {
			out := verifyTabText(t, buildPreciseAnchorReport(), size.width, size.height)
			block := false
			for _, line := range strings.Split(out, "\n") {
				switch {
				case strings.Contains(line, "the measured figure, in full"):
					block = true
					continue
				case block && strings.Contains(line, "verdict:"):
					block = false
				}
				// View repeats header/footer chrome on every page. Those rows are
				// intentionally clipped, but the value lines in this block are
				// indented and must be wrapped in full.
				if block && strings.HasPrefix(line, "    ") && strings.Contains(line, "…") {
					t.Errorf("a line of the measured-figure block is clipped: %q", strings.TrimSpace(line))
				}
			}
			if !strings.Contains(out, "the measured figure, in full") {
				t.Errorf("the measured-figure block is not on the Verify tab at all:\n%s", out)
			}
		})
	}
}

// TestVerifyTabCaveatsAreWrappedNotClipped covers the other frame the figures
// were lost on. A caveat is a whole sentence explaining why a number above
// should be read differently, and the one carrying the anchor's cache split had
// its numbers in the part that was cut off.
func TestVerifyTabCaveatsAreWrappedNotClipped(t *testing.T) {
	out := verifyTabText(t, buildPreciseAnchorReport(), 100, 30)
	if !strings.Contains(out, "anchor-warm-cache") {
		t.Fatalf("the warm-cache caveat is not rendered at all:\n%s", out)
	}
	// The last words of the sentence. A clipped caveat keeps its opening and
	// loses precisely the part that qualifies the figure.
	if !strings.Contains(out, "does not change the prompt's size.") {
		t.Errorf("the caveat lost its ending to the pane's width:\n%s", out)
	}
}

// TestVerifyTabWithoutAnAnchorSaysSoInTheBlock keeps the block honest on the
// sessions that have no measurement: it must not print an empty heading and let
// the reader assume the figures are elsewhere.
func TestVerifyTabWithoutAnAnchorSaysSoInTheBlock(t *testing.T) {
	rep := buildPreciseAnchorReport()
	rep.Anchor = nil
	rep.Reconcile()

	out := verifyTabText(t, rep, 120, 30)
	if !strings.Contains(out, "no measured figure") {
		t.Errorf("the measured-figure block says nothing when there is no measurement:\n%s", out)
	}
}

// TestLegendSaysDiskDerivedRowsAreAsOfNow covers the other half of the honesty
// gap. The panel reports what WOULD load from disk right now; a session already
// running is still sending the copy it booted with. Trim a CLAUDE.md and the
// figure here drops while the running session's prefix does not, so the reader
// is shown a saving they have not made. The screen has to say which question it
// is answering.
func TestLegendSaysDiskDerivedRowsAreAsOfNow(t *testing.T) {
	joined := strings.Join(contextLegendLines(), "\n")
	if !strings.Contains(joined, "as of now on disk") {
		t.Errorf("the column legend does not say that a RECON row describes the file as it is now:\n%s", joined)
	}
	if !strings.Contains(joined, "restart") {
		t.Errorf("the legend does not say what makes the two agree again:\n%s", joined)
	}
}

// TestContextExactAmount pins the formatter the block relies on. Rounding is
// right for a dense column and wrong for a figure offered for verification.
func TestContextExactAmount(t *testing.T) {
	cases := map[int]string{
		0:       "0",
		7:       "7",
		999:     "999",
		1000:    "1,000",
		27013:   "27,013",
		1234567: "1,234,567",
		-27013:  "-27,013",
	}
	for in, want := range cases {
		if got := contextExactAmount(in); got != want {
			t.Errorf("contextExactAmount(%d) = %q, want %q", in, got, want)
		}
	}
	if contextTokenAmount(27013) == contextExactAmount(27013) {
		t.Error("the exact and the rounded formatter agree on 27013: one of them is not doing its job")
	}
}
