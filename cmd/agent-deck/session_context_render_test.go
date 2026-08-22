package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
	"github.com/asheshgoplani/agent-deck/internal/ctxinspect/ctxtext"
)

// contextTestReport builds a report exercising every rendering path: a measured
// anchor, an estimated item, an on-demand item with a potential cost, an
// item nobody can act on, and a residual. It is deliberately assembled by hand
// rather than by an adapter so these tests fail on a renderer change and not on
// a harness change.
func contextTestReport(t *testing.T) *ctxinspect.Report {
	t.Helper()
	rep := &ctxinspect.Report{
		Harness:     "claude",
		Adapter:     "claude",
		SessionID:   "sess-abc",
		ProjectPath: "/tmp/proj",
		Model:       "claude-test-1",
		Window:      ctxinspect.WindowInfo{Tokens: 200_000, Source: ctxinspect.WindowModelDefault, Detail: "claude-test-1"},
		Basis:       ctxinspect.BasisObserved,
		Anchor: &ctxinspect.Anchor{
			Tokens: ctxinspect.Measured(100_000, "first assistant turn usage"),
			Source: "first assistant turn",
		},
		Categories: []ctxinspect.Category{
			{
				Name:  "memory-files",
				Title: "memory files",
				Items: []ctxinspect.Item{
					{
						ID:      "memory:global",
						Label:   "~/.claude/CLAUDE.md",
						Detail:  "12.0 KB",
						Content: ctxinspect.ReconstructedContent(ctxinspect.KindMarkdown, "global memory body\n", "rebuilt by re-running the discovery walk"),
						Load:    ctxinspect.LoadedNowCost(ctxinspect.Estimated(30_000, "chars/4: markdown")),
						Origin:  ctxinspect.OriginUserConfig,
						Lever:   ctxinspect.FileLever("/home/u/.claude/CLAUDE.md", "user memory, loaded for every session"),
					},
					{
						ID:      "memory:project",
						Label:   "./CLAUDE.md",
						Content: ctxinspect.ReconstructedContent(ctxinspect.KindMarkdown, "project memory body\n", "rebuilt by re-running the discovery walk"),
						Load:    ctxinspect.LoadedNowCost(ctxinspect.Estimated(10_000, "chars/4: markdown")),
						Origin:  ctxinspect.OriginProject,
						Lever:   ctxinspect.FileLever("/tmp/proj/CLAUDE.md", "project memory"),
					},
				},
			},
			{
				Name:  "skills",
				Title: "skills",
				Notes: []string{"only the listing line is loaded; a skill body loads when it is invoked"},
				Items: []ctxinspect.Item{
					{
						ID:      "skill:dataviz",
						Label:   "dataviz",
						Content: ctxinspect.CapturedContent(ctxinspect.KindListing, "dataviz: charts\n"),
						Load: ctxinspect.OnDemandCost(
							ctxinspect.Estimated(100, "chars/4: listing"),
							ctxinspect.Estimated(4_000, "chars/4: markdown"),
						),
						Origin: ctxinspect.OriginUserConfig,
						Lever:  ctxinspect.DirLever("/home/u/.claude/skills/dataviz", "delete the skill directory to stop it being listed"),
					},
					{
						ID:      "skill:builtin",
						Label:   "builtin-skill",
						Content: ctxinspect.CapturedContent(ctxinspect.KindListing, "builtin-skill: shipped\n"),
						Load:    ctxinspect.LoadedNowCost(ctxinspect.Estimated(50, "chars/4: listing")),
						Origin:  ctxinspect.OriginHarnessBuiltin,
						Lever:   ctxinspect.ImmovableLever("shipped inside the agent binary"),
					},
				},
			},
		},
		Capabilities: ctxinspect.Capabilities{
			Adapter:   "claude",
			CanAnchor: true,
			Categories: []ctxinspect.CategoryCapability{
				{Name: "memory-files", Title: "memory files", Text: ctxinspect.TextReconstructed, Token: ctxinspect.TokenEstimated, Note: "rebuilt from the discovery walk"},
				{Name: "skills", Title: "skills", Text: ctxinspect.TextCaptured, Token: ctxinspect.TokenEstimated, Note: "read from the skill listing record"},
			},
		},
		History: &ctxinspect.HistoryLine{Tokens: ctxinspect.Measured(43_000, "last turn usage"), Turns: 12},
	}
	rep.Reconcile()
	if err := rep.Validate(); err != nil {
		t.Fatalf("test fixture is not a valid report: %v", err)
	}
	return rep
}

func contextTestView(t *testing.T) contextView {
	t.Helper()
	return contextView{
		Ref:     "my-project",
		Title:   "my-project",
		Profile: "personal",
		Tool:    "claude",
		Report:  contextTestReport(t),
	}
}

func TestFormatTokenAmount(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want string
	}{
		{"zero", 0, "0"},
		{"units", 512, "512"},
		{"thousands", 1_234, "1.2k"},
		{"tens of thousands", 48_100, "48.1k"},
		{"millions", 1_000_000, "1.0M"},
		{"negative keeps its sign", -12_300, "-12.3k"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatTokenAmount(tc.in); got != tc.want {
				t.Fatalf("formatTokenAmount(%d) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFormatTokenCountMarksProvenance(t *testing.T) {
	tests := []struct {
		name string
		in   ctxinspect.TokenCount
		want string
	}{
		{"measured has no marker", ctxinspect.Measured(1_200, "usage"), "1.2k"},
		{"computed has no marker", ctxinspect.Computed(1_200, "tokenizer"), "1.2k"},
		{"estimated is marked", ctxinspect.Estimated(1_200, "chars/4"), "~1.2k"},
		{"residual is marked", ctxinspect.ResidualTokens(1_200, "anchor − Σ"), "~1.2k"},
		{"unknown is an em dash, never zero", ctxinspect.UnknownTokens("no accounting"), "—"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatTokenCount(tc.in); got != tc.want {
				t.Fatalf("formatTokenCount() = %q, want %q", got, tc.want)
			}
		})
	}
}

// An unknown must never render as "0": that is the exact failure the feature
// exists to prevent.
func TestFormatTokenCountNeverRendersUnknownAsZero(t *testing.T) {
	if got := formatTokenCount(ctxinspect.UnknownTokens("no accounting")); strings.Contains(got, "0") {
		t.Fatalf("an unknown count rendered as %q, which contains a digit", got)
	}
}

func TestFormatTotalTokensMarksLowerBound(t *testing.T) {
	if got := formatTotalTokens(1_000, true); got != "1.0k" {
		t.Fatalf("complete total rendered as %q", got)
	}
	got := formatTotalTokens(1_000, false)
	if !strings.HasPrefix(got, "≥") {
		t.Fatalf("incomplete total rendered as %q, want a ≥ prefix so it cannot be read as a total", got)
	}
	// "≥0" would dress up "we know nothing" as a figure.
	if got := formatTotalTokens(0, false); got != "—" {
		t.Fatalf("a wholly unknown total rendered as %q, want an em dash", got)
	}
}

func TestGaugeBarClampsForDisplay(t *testing.T) {
	tests := []struct {
		name string
		pct  float64
		want string
	}{
		{"empty", 0, "[..........]"},
		{"half", 50, "[#####.....]"},
		{"full", 100, "[##########]"},
		{"over-full clamps", 150, "[##########]"},
		{"negative clamps", -20, "[..........]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := gaugeBar(tc.pct, 10); got != tc.want {
				t.Fatalf("gaugeBar(%v) = %q, want %q", tc.pct, got, tc.want)
			}
		})
	}
}

func TestRenderContextOverviewShowsEveryCategoryAndTheResidual(t *testing.T) {
	v := contextTestView(t)
	out := renderContextOverview(v)

	for _, want := range []string{
		"context · my-project · tool claude · adapter claude",
		"window:  200.0k tokens (model-default: claude-test-1)",
		"basis:   observed",
		"memory files (2 items, 2 actionable)",
		"skills (2 items, 1 actionable)",
		"system prompt + built-in tool schemas",
		"history: 43.0k over 12 turns",
		"reconciliation: OK",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("overview is missing %q\n---\n%s", want, out)
		}
	}
}

// The gauge must equal the anchor exactly: the residual is defined as
// anchor − Σ(attributed), so a mismatch means a renderer, not an adapter, lost
// a number.
func TestRenderContextOverviewGaugeEqualsTheAnchor(t *testing.T) {
	v := contextTestView(t)
	fixed, complete := v.Report.FixedTotal()
	if !complete {
		t.Fatalf("fixture total is incomplete")
	}
	if fixed != 100_000 {
		t.Fatalf("fixed total = %d, want the anchor 100000", fixed)
	}
	if !strings.Contains(renderContextOverview(v), "100.0k / 200.0k  (50.0%)") {
		t.Fatalf("overview did not render the gauge against the anchor:\n%s", renderContextOverview(v))
	}
}

func TestRenderContextOverviewOmitsPercentWithoutAWindow(t *testing.T) {
	v := contextTestView(t)
	v.Report.Window = ctxinspect.WindowInfo{Source: ctxinspect.WindowUnknown, Detail: "no context-window size is known for model \"claude-test-9\""}
	out := renderContextOverview(v)
	if strings.Contains(out, "%)") {
		t.Fatalf("overview printed a percentage of an unknown denominator:\n%s", out)
	}
	// An unknown is allowed. A dead end is not: the reason and the one line
	// that fixes it both belong under the gauge, not in a caveat block far
	// below the figure whose absence prompted the question.
	if !strings.Contains(out, "/ ?") {
		t.Fatalf("the gauge must keep its shape and show the absolute total:\n%s", out)
	}
	if !strings.Contains(out, "claude-test-9") {
		t.Fatalf("overview did not say WHY the window is unknown:\n%s", out)
	}
	if !strings.Contains(out, ctxtext.WindowEnvVar) {
		t.Fatalf("overview did not say how to supply the window:\n%s", out)
	}
}

// One report, two renderers, one sentence. They had drifted: this one printed
// an explanation and the TUI pager printed the bare word "unknown".
func TestWindowLineIsTheSharedFormatter(t *testing.T) {
	for _, w := range []ctxinspect.WindowInfo{
		{},
		{Source: ctxinspect.WindowUnknown, Detail: "nothing to go on"},
		{Tokens: 200_000, Source: ctxinspect.WindowModelDefault, Detail: "model prefix \"claude-test\""},
		{Tokens: 1_000_000, Source: ctxinspect.WindowModelFamily, Detail: "assumed from the claude-test family"},
	} {
		if got, want := windowLine(w), ctxtext.WindowLine(w); got != want {
			t.Fatalf("windowLine(%+v) = %q, want the shared %q", w, got, want)
		}
	}
	if !strings.Contains(windowLine(ctxinspect.WindowInfo{}), ctxtext.WindowEnvVar) {
		t.Fatal("an unknown window must always carry the one line that fixes it")
	}
}

// A denominator nothing measured must not produce a percentage that reads as
// though something did.
func TestRenderContextOverviewMarksAnAssumedWindow(t *testing.T) {
	v := contextTestView(t)
	v.Report.Window = ctxinspect.WindowInfo{Tokens: 1_000_000, Source: ctxinspect.WindowModelFamily, Detail: "assumed from the claude-test family"}
	out := renderContextOverview(v)
	if !strings.Contains(out, "(≈") {
		t.Fatalf("a percentage over an assumed window must be marked:\n%s", out)
	}
	if !strings.Contains(out, "assumed denominator") {
		t.Fatalf("the mark must be explained beneath the gauge:\n%s", out)
	}
	if !strings.Contains(out, ctxtext.WindowEnvVar) {
		t.Fatalf("an assumed window must say how to replace the assumption:\n%s", out)
	}
}

func TestRenderContextBreakdownRanksActionableFirstThenCost(t *testing.T) {
	v := contextTestView(t)
	ranked := contextRankedItems(v.Report)

	var ids []string
	for _, ri := range ranked {
		ids = append(ids, ri.Item.ID)
	}
	want := []string{
		"memory:global",  // actionable, loaded, 30k
		"memory:project", // actionable, loaded, 10k
		"skill:dataviz",  // actionable, on-demand
		"skill:builtin",  // immovable
		"unaccounted",    // residual, pinned last
	}
	if len(ids) != len(want) {
		t.Fatalf("ranking = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ranking = %v, want %v", ids, want)
		}
	}
}

func TestRenderContextBreakdownTruncatesWithoutAll(t *testing.T) {
	v := contextTestView(t)
	cat := ctxinspect.Category{Name: "filler", Title: "filler"}
	for i := 0; i < defaultBreakdownRows+5; i++ {
		cat.Items = append(cat.Items, ctxinspect.Item{
			ID:      "filler:" + string(rune('a'+i%26)) + string(rune('0'+i/26)),
			Label:   "filler item",
			Content: ctxinspect.CapturedContent(ctxinspect.KindListing, "x"),
			Load:    ctxinspect.LoadedNowCost(ctxinspect.Estimated(1, "chars/4: listing")),
			Origin:  ctxinspect.OriginProject,
			Lever:   ctxinspect.FileLever("/tmp/proj/filler", "filler"),
		})
	}
	v.Report.Categories = append(v.Report.Categories, cat)

	if got := renderContextBreakdown(v, false); !strings.Contains(got, "pass --all to see them") {
		t.Fatalf("breakdown did not offer --all:\n%s", got)
	}
	if got := renderContextBreakdown(v, true); strings.Contains(got, "pass --all to see them") {
		t.Fatalf("--all still truncated the breakdown")
	}
}

func TestFindContextItem(t *testing.T) {
	rep := contextTestReport(t)

	t.Run("exact id", func(t *testing.T) {
		ri, err := findContextItem(rep, "memory:global")
		if err != nil {
			t.Fatalf("findContextItem: %v", err)
		}
		if ri.Category != "memory-files" {
			t.Fatalf("category = %q, want memory-files", ri.Category)
		}
	})

	t.Run("unique prefix", func(t *testing.T) {
		ri, err := findContextItem(rep, "skill:d")
		if err != nil {
			t.Fatalf("findContextItem: %v", err)
		}
		if ri.Item.ID != "skill:dataviz" {
			t.Fatalf("id = %q, want skill:dataviz", ri.Item.ID)
		}
	})

	t.Run("ambiguous prefix is refused", func(t *testing.T) {
		_, err := findContextItem(rep, "memory:")
		var ambiguous errContextItemAmbiguous
		if !errors.As(err, &ambiguous) {
			t.Fatalf("expected an ambiguity error, got %v", err)
		}
		if len(ambiguous.Matches) != 2 {
			t.Fatalf("ambiguity error listed %v, want both memory ids", ambiguous.Matches)
		}
	})

	t.Run("unknown id", func(t *testing.T) {
		var notFound errContextItemNotFound
		if _, err := findContextItem(rep, "nope"); !errors.As(err, &notFound) {
			t.Fatalf("expected a not-found error, got %v", err)
		}
	})

	t.Run("the residual is addressable", func(t *testing.T) {
		if _, err := findContextItem(rep, "unaccounted"); err != nil {
			t.Fatalf("the residual row must be addressable: %v", err)
		}
	})
}

func TestRenderContextItemShowsBothProvenanceAxes(t *testing.T) {
	v := contextTestView(t)
	ri, err := findContextItem(v.Report, "memory:global")
	if err != nil {
		t.Fatalf("findContextItem: %v", err)
	}
	out := renderContextItem(v, ri)
	for _, want := range []string{
		"reconstructed —",
		"estimated —",
		"edit /home/u/.claude/CLAUDE.md",
		"global memory body",
		"--- end ---",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("item view is missing %q\n---\n%s", want, out)
		}
	}
}

// Absent text must be stated, never rendered as an empty body: an empty body
// reads as "there is nothing here", which is a different claim.
func TestRenderContextContentStatesAbsenceExplicitly(t *testing.T) {
	out := renderContextContent(ctxinspect.AbsentContent("the harness never writes this to disk"))
	if !strings.Contains(out, "content unavailable") {
		t.Fatalf("absent content did not say so:\n%s", out)
	}
	if !strings.Contains(out, "the harness never writes this to disk") {
		t.Fatalf("absent content dropped its explanation:\n%s", out)
	}
}

func TestRenderContextContentMarksTruncation(t *testing.T) {
	c := ctxinspect.CapturedContent(ctxinspect.KindProse, "abc")
	c.Chars = 5_000
	c.Truncated = true
	out := renderContextContent(c)
	if !strings.Contains(out, "TRUNCATED") || !strings.Contains(out, "of 5000 chars") {
		t.Fatalf("truncated content was not marked as such:\n%s", out)
	}
}

func TestRenderContextVerifyReportsTheArithmetic(t *testing.T) {
	v := contextTestView(t)
	out := renderContextVerify(v)
	for _, want := range []string{
		"anchor",
		"provider-measured",
		"unaccounted",
		"anchor − Σ(attributed); never clamped to zero",
		"verdict:  OK",
		"coverage:",
		"no violations",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("verify view is missing %q\n---\n%s", want, out)
		}
	}
}

// The report reads the files as they are now; a session already running is
// still sending the copy it booted with. Trim a CLAUDE.md and the figure here
// drops while the running session's prefix does not, so the reader is shown a
// saving they have not made. The legend has to say which question the RECON
// rows answer, and what makes the two agree again.
func TestContextLegendSaysDiskDerivedRowsAreAsOfNow(t *testing.T) {
	out := contextLegend("")
	if !strings.Contains(out, "as of now on disk") {
		t.Errorf("the legend does not say that a RECON row describes the file as it is now:\n%s", out)
	}
	if !strings.Contains(out, "restart") {
		t.Errorf("the legend does not say what makes the two agree again:\n%s", out)
	}
}

// The one figure the report labels MEASURED must be checkable against the
// provider's own accounting, and a rounded rendering is not checkable: "47.0k"
// matches 46,957 and 47,013 equally well. The verify frame therefore states the
// anchor to the digit, with its source and the subtraction that produces the
// residual, mirroring the pager's Verify tab. The fixture's figures are
// deliberately not round so a rounded render cannot accidentally pass.
func TestRenderContextVerifyStatesTheAnchorAtFullPrecision(t *testing.T) {
	rep := contextTestReport(t)
	rep.Anchor = &ctxinspect.Anchor{
		Tokens: ctxinspect.Measured(46_957, "message.usage"),
		Source: "first assistant turn: input 1201 + cache creation 5400 + cache read 40356",
	}
	rep.Reconcile()
	if err := rep.Validate(); err != nil {
		t.Fatalf("fixture stopped being a valid report: %v", err)
	}
	v := contextTestView(t)
	v.Report = rep

	out := renderContextVerify(v)
	for _, want := range []string{
		"the measured figure, in full:",
		"46,957 tokens",
		// The component sums, including the cache-read share, must be readable
		// where the exact total is, not inferable from a rounded column.
		"cache read 40356",
		"46,957 measured − 40,150 attributed = 6,807 unattributed remainder",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("verify view is missing %q: the measured figure must be verifiable to the digit\n---\n%s", want, out)
		}
	}
}

// A session with no measurement must say so inside the block, not print an
// empty heading and let the reader assume the figures are elsewhere.
func TestRenderContextVerifyFullPrecisionBlockWithoutAnchor(t *testing.T) {
	rep := contextTestReport(t)
	rep.Anchor = nil
	rep.Reconcile()
	v := contextTestView(t)
	v.Report = rep

	out := renderContextVerify(v)
	if !strings.Contains(out, "the measured figure, in full:") {
		t.Fatalf("the full-precision block disappeared with the anchor:\n%s", out)
	}
	if !strings.Contains(out, "none: nothing this session recorded measured its fixed prefix") {
		t.Fatalf("the block does not say why there is no measured figure:\n%s", out)
	}
}

// A reconciliation failure must be loud. Clamping the negative, or rendering it
// as a clean total, is how a plausible-looking lie ships.
func TestRenderContextVerifySurfacesAFailedReconciliation(t *testing.T) {
	rep := contextTestReport(t)
	rep.Anchor = &ctxinspect.Anchor{Tokens: ctxinspect.Measured(1_000, "first assistant turn usage"), Source: "first assistant turn"}
	rep.Reconcile()
	_ = rep.Validate()

	if rep.Reconciliation.Status != ctxinspect.ReconFailed {
		t.Fatalf("status = %v, want failed", rep.Reconciliation.Status)
	}
	v := contextTestView(t)
	v.Report = rep
	out := renderContextVerify(v)
	if !strings.Contains(out, "FAILED") {
		t.Fatalf("verify view hid a failed reconciliation:\n%s", out)
	}
	if !strings.Contains(out, "-39.") {
		t.Fatalf("verify view clamped the negative residual:\n%s", out)
	}
	if strings.Contains(out, "~-") {
		t.Fatalf("a negative residual must not be marked as an estimate:\n%s", out)
	}
}

func TestContextTokenAccountingUnsupported(t *testing.T) {
	tests := []struct {
		name            string
		caps            ctxinspect.Capabilities
		wantUnsupported bool
	}{
		{
			name:            "an anchor means accounting exists",
			caps:            ctxinspect.Capabilities{Adapter: "claude", CanAnchor: true},
			wantUnsupported: false,
		},
		{
			name: "one priceable category means accounting exists",
			caps: ctxinspect.Capabilities{Adapter: "claude", Categories: []ctxinspect.CategoryCapability{
				{Name: "memory-files", Token: ctxinspect.TokenEstimated},
			}},
			wantUnsupported: false,
		},
		{
			name: "every category unknown and no anchor is unsupported",
			caps: ctxinspect.Capabilities{Adapter: "generic", Categories: []ctxinspect.CategoryCapability{
				{Name: "instruction-files", Token: ctxinspect.TokenUnknown},
				{Name: "mcp", Token: ctxinspect.TokenUnknown},
			}},
			wantUnsupported: true,
		},
		{
			name:            "no categories at all is unsupported",
			caps:            ctxinspect.Capabilities{Adapter: "generic"},
			wantUnsupported: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rep := &ctxinspect.Report{Harness: "x", Capabilities: tc.caps}
			reason, unsupported := contextTokenAccountingUnsupported(rep)
			if unsupported != tc.wantUnsupported {
				t.Fatalf("unsupported = %v, want %v", unsupported, tc.wantUnsupported)
			}
			if unsupported && strings.TrimSpace(reason) == "" {
				t.Fatal("an unsupported verdict must carry a reason")
			}
			if !unsupported && reason != "" {
				t.Fatalf("a supported verdict must carry no reason, got %q", reason)
			}
		})
	}
}

// The generic-adapter path must print the honest line and a populated
// inventory, not an error.
func TestRenderContextOverviewPrintsUnsupportedLineForAGenericReport(t *testing.T) {
	rep := &ctxinspect.Report{
		Harness: "cursor-agent",
		Adapter: "generic",
		Basis:   ctxinspect.BasisProjected,
		Window:  ctxinspect.WindowInfo{Source: ctxinspect.WindowUnknown},
		Categories: []ctxinspect.Category{{
			Name:  "instruction-files",
			Title: "instruction files",
			Items: []ctxinspect.Item{{
				ID:      "instr:0",
				Label:   "AGENTS.md",
				Content: ctxinspect.ReconstructedContent(ctxinspect.KindMarkdown, "hello\n", "found by walking the chain"),
				Load:    ctxinspect.Load{State: ctxinspect.LoadedNow, Actual: ctxinspect.UnknownTokens("no accounting")},
				Origin:  ctxinspect.OriginProject,
				Lever:   ctxinspect.FileLever("/tmp/proj/AGENTS.md", "project instructions"),
			}},
		}},
		Capabilities: ctxinspect.Capabilities{
			Adapter: "generic",
			Categories: []ctxinspect.CategoryCapability{
				{Name: "instruction-files", Token: ctxinspect.TokenUnknown},
			},
		},
	}
	rep.Reconcile()
	_ = rep.Validate()

	v := contextView{Ref: "x", Title: "x", Profile: "personal", Tool: "cursor-agent", Report: rep}
	out := renderContextOverview(v)
	if !strings.Contains(out, "token accounting unsupported for cursor-agent") {
		t.Fatalf("generic overview did not print the unsupported line:\n%s", out)
	}
	if !strings.Contains(out, "AGENTS.md") {
		t.Fatalf("generic overview dropped the inventory:\n%s", out)
	}
	if !strings.Contains(out, "—") {
		t.Fatalf("generic overview did not render an em dash for the unknown cost:\n%s", out)
	}
}

func TestContextExitStatus(t *testing.T) {
	clean := contextTestReport(t)
	broken := contextTestReport(t)
	broken.Anchor = &ctxinspect.Anchor{Tokens: ctxinspect.Measured(1, "usage"), Source: "first assistant turn"}
	broken.Reconcile()
	_ = broken.Validate()

	tests := []struct {
		name   string
		rep    *ctxinspect.Report
		strict bool
		want   int
	}{
		{"clean, lenient", clean, false, contextExitOK},
		{"clean, strict", clean, true, contextExitOK},
		{"unreconciled, lenient stays 0", broken, false, contextExitOK},
		{"unreconciled, strict fails", broken, true, contextExitUnreconciled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := contextExitStatus(tc.rep, tc.strict); got != tc.want {
				t.Fatalf("contextExitStatus = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestLeverLine(t *testing.T) {
	tests := []struct {
		name string
		in   ctxinspect.Lever
		want string
	}{
		{"file", ctxinspect.FileLever("/a/b", "why"), "edit /a/b — why"},
		{"dir", ctxinspect.DirLever("/a/b", "why"), "delete directory /a/b — why"},
		{"command", ctxinspect.CommandLever("agent-deck mcp detach s x", "why"), "run: agent-deck mcp detach s x — why"},
		{"immovable", ctxinspect.ImmovableLever("shipped in the binary"), "immovable — shipped in the binary"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := leverLine(tc.in); got != tc.want {
				t.Fatalf("leverLine = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLeverLineRendersALineRange(t *testing.T) {
	l := ctxinspect.FileLever("/a/b", "why")
	l.LineRange = [2]int{10, 20}
	if got := leverLine(l); !strings.Contains(got, "(lines 10–20)") {
		t.Fatalf("leverLine = %q, want a line range", got)
	}
}

func TestWriteContextTableLeavesNoTrailingWhitespace(t *testing.T) {
	var b strings.Builder
	writeContextTable(&b, [][]string{
		{"A", "BBBB"},
		{"AAAA", "B"},
	}, "  ")
	for _, line := range strings.Split(strings.TrimRight(b.String(), "\n"), "\n") {
		if line != strings.TrimRight(line, " ") {
			t.Fatalf("line %q has trailing whitespace, which makes golden output brittle", line)
		}
	}
}
