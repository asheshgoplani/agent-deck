package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
	"github.com/asheshgoplani/agent-deck/internal/ctxinspect/ctxtext"
)

// buildContextTestReport returns a report shaped like a real Claude Code
// inspection: two adapter-declared categories, one immovable item, one item
// with no token count at all, a deferred item with a larger potential cost, a
// segmented item, and a measured anchor that leaves a residual.
func buildContextTestReport() *ctxinspect.Report {
	memory := ctxinspect.Category{
		Name:        "memory-files",
		Title:       "memory files",
		Description: "instruction files discovered up the directory chain",
		Items: []ctxinspect.Item{
			{
				ID:      "memory:global",
				Label:   "~/.claude/CLAUDE.md",
				Detail:  "global",
				Content: ctxinspect.ReconstructedContent(ctxinspect.KindProse, "global rules\nline two\n", "rebuilt from disk"),
				Load:    ctxinspect.LoadedNowCost(ctxinspect.Estimated(4000, "chars/4")),
				Origin:  ctxinspect.OriginUserConfig,
				Lever:   ctxinspect.FileLever("/home/u/.claude/CLAUDE.md", "loaded for every session"),
			},
			{
				ID:      "memory:project",
				Label:   "./CLAUDE.md",
				Detail:  "project",
				Content: ctxinspect.ReconstructedContent(ctxinspect.KindProse, "project rules\n", "rebuilt from disk"),
				Load:    ctxinspect.LoadedNowCost(ctxinspect.Estimated(1000, "chars/4")),
				Origin:  ctxinspect.OriginProject,
				Lever:   ctxinspect.FileLever("/repo/CLAUDE.md", "in the project root"),
			},
		},
	}

	skills := ctxinspect.Category{
		Name:  "skills",
		Title: "skills",
		Items: []ctxinspect.Item{
			{
				ID:      "skill:dataviz",
				Label:   "dataviz",
				Content: ctxinspect.CapturedContent(ctxinspect.KindProse, "dataviz — charts"),
				Load: ctxinspect.OnDemandCost(
					ctxinspect.Estimated(100, "chars/4"),
					ctxinspect.Estimated(9000, "chars/4"),
				),
				Origin: ctxinspect.OriginUserConfig,
				Lever:  ctxinspect.DirLever("/home/u/.claude/skills/dataviz", "listed in the skill catalogue"),
			},
			{
				ID:      "skill:unpriced",
				Label:   "unpriced",
				Content: ctxinspect.AbsentContent("the harness records no text for this skill"),
				Load:    ctxinspect.Load{State: ctxinspect.LoadedNow, Actual: ctxinspect.UnknownTokens("no figure is recoverable")},
				Origin:  ctxinspect.OriginPlugin,
				Lever:   ctxinspect.CommandLever("agent-deck mcp detach demo telegram", "attached by agent-deck"),
			},
		},
	}

	systemPrompt := ctxinspect.Category{
		Name:  "system-prompt",
		Title: "system prompt",
		Items: []ctxinspect.Item{
			{
				ID:      "system:base",
				Label:   "base system prompt",
				Content: ctxinspect.CapturedContent(ctxinspect.KindProse, "you are an agent\nbe helpful\n"),
				Load:    ctxinspect.LoadedNowCost(ctxinspect.Estimated(5000, "chars/4")),
				Origin:  ctxinspect.OriginHarnessBuiltin,
				Lever:   ctxinspect.ImmovableLever("shipped inside the agent binary"),
				Children: []ctxinspect.Item{
					{
						ID:      "system:base:0",
						Label:   "preamble",
						Content: ctxinspect.CapturedContent(ctxinspect.KindProse, "you are an agent\n"),
						Load:    ctxinspect.LoadedNowCost(ctxinspect.Estimated(3000, "chars/4")),
						Origin:  ctxinspect.OriginHarnessBuiltin,
						Lever:   ctxinspect.ImmovableLever("shipped inside the agent binary"),
					},
					{
						ID:      "system:base:1",
						Label:   "tone",
						Content: ctxinspect.CapturedContent(ctxinspect.KindProse, "be helpful\n"),
						Load:    ctxinspect.LoadedNowCost(ctxinspect.Estimated(2000, "chars/4")),
						Origin:  ctxinspect.OriginHarnessBuiltin,
						Lever:   ctxinspect.ImmovableLever("shipped inside the agent binary"),
					},
				},
			},
		},
	}

	rep := &ctxinspect.Report{
		Harness:     "claude",
		Adapter:     "claude",
		SessionID:   "abcd-1234",
		ProjectPath: "/repo",
		Model:       "claude-test",
		Window:      ctxinspect.WindowInfo{Tokens: 200000, Source: ctxinspect.WindowModelDefault, Detail: "claude-test"},
		Basis:       ctxinspect.BasisObserved,
		Anchor: &ctxinspect.Anchor{
			Tokens: ctxinspect.Measured(20000, "first assistant turn"),
			Source: "first assistant turn",
		},
		Categories: []ctxinspect.Category{memory, skills, systemPrompt},
		History:    &ctxinspect.HistoryLine{Tokens: ctxinspect.Measured(4321, "last turn"), Turns: 7},
		Capabilities: ctxinspect.Capabilities{
			Adapter:           "claude",
			CanAnchor:         true,
			CanVerbatimSystem: false,
			Categories: []ctxinspect.CategoryCapability{
				{Name: "memory-files", Title: "memory files", Text: ctxinspect.TextReconstructed, Token: ctxinspect.TokenEstimated, Note: "rebuilt from disk"},
				{Name: "skills", Title: "skills", Text: ctxinspect.TextCaptured, Token: ctxinspect.TokenEstimated, Note: "skill_listing record"},
			},
		},
	}
	rep.Reconcile()
	return rep
}

// buildContextUnsupportedReport returns the honest-unsupported shape: a
// populated inventory in which no figure is knowable.
func buildContextUnsupportedReport() *ctxinspect.Report {
	rep := &ctxinspect.Report{
		Harness: "aider",
		Adapter: "generic",
		Basis:   ctxinspect.BasisProjected,
		Categories: []ctxinspect.Category{{
			Name:  "instruction-files",
			Title: "instruction files",
			Items: []ctxinspect.Item{{
				ID:      "instructions:0",
				Label:   "/repo/AGENTS.md",
				Content: ctxinspect.AbsentContent("this harness records nothing agent-deck can read"),
				Load:    ctxinspect.Load{State: ctxinspect.LoadedNow, Actual: ctxinspect.UnknownTokens("no token accounting")},
				Origin:  ctxinspect.OriginProject,
				Lever:   ctxinspect.FileLever("/repo/AGENTS.md", "found by walking the project tree"),
			}},
		}},
		Capabilities: ctxinspect.Capabilities{
			Adapter: "generic",
			Categories: []ctxinspect.CategoryCapability{
				{Name: "instruction-files", Title: "instruction files", Text: ctxinspect.TextAbsent, Token: ctxinspect.TokenUnknown, Note: "no accounting exists"},
			},
		},
	}
	rep.Reconcile()
	return rep
}

// newContextPagerForTest returns a pager already showing a report.
func newContextPagerForTest(t *testing.T, rep *ctxinspect.Report) *ContextPager {
	t.Helper()
	p := NewContextPager()
	p.Show("demo", "session-1", "claude", 120, 30)
	p.SetReport(rep, []string{"transcript resolved from the per-instance config dir"})
	return p
}

func TestContextPagerShowStartsLoading(t *testing.T) {
	p := NewContextPager()
	if p.IsVisible() {
		t.Fatal("a new pager must be hidden")
	}
	p.Show("demo", "session-1", "claude", 100, 24)
	if !p.IsVisible() || !p.Loading() {
		t.Fatal("Show must open the pager in a loading state")
	}
	if p.SessionID() != "session-1" {
		t.Fatalf("SessionID = %q, want session-1", p.SessionID())
	}
	out := ansi.Strip(p.View())
	if !strings.Contains(out, "Reading what this session is being sent") {
		t.Fatalf("loading view is missing its message:\n%s", out)
	}
}

func TestContextPagerNilReportIsAnError(t *testing.T) {
	p := NewContextPager()
	p.Show("demo", "session-1", "claude", 100, 24)
	p.SetReport(nil, nil)
	if p.Loading() {
		t.Fatal("a nil report must end the loading state")
	}
	out := ansi.Strip(p.View())
	if !strings.Contains(out, "Could not inspect this context") {
		t.Fatalf("a nil report must render as a failure, not an empty inventory:\n%s", out)
	}
}

func TestContextPagerErrorStateNeverShowsZeroes(t *testing.T) {
	p := NewContextPager()
	p.Show("demo", "session-1", "claude", 100, 24)
	p.SetError("transcript unreadable")
	out := ansi.Strip(p.View())
	if !strings.Contains(out, "transcript unreadable") {
		t.Fatalf("the failure cause must be shown:\n%s", out)
	}
	if strings.Contains(out, "fixed startup overhead") {
		t.Fatalf("a failed inspection must not render a gauge:\n%s", out)
	}
}

func TestContextPagerRankingPutsActionableAndExpensiveFirst(t *testing.T) {
	rep := buildContextTestReport()
	ranked := contextRankAll(rep)

	if len(ranked) == 0 {
		t.Fatal("ranking produced no rows")
	}
	if ranked[0].Item.ID != "memory:global" {
		t.Fatalf("first row = %q, want the most expensive actionable item memory:global", ranked[0].Item.ID)
	}

	// A rollup parent is replaced by its children so a group header and its
	// members can never both be read as a cost.
	for _, ri := range ranked {
		if ri.Item.Rollup {
			t.Fatalf("rollup parent %q leaked into the ranking", ri.Item.ID)
		}
	}

	// Actionable rows all precede immovable ones.
	seenImmovable := false
	for _, ri := range ranked {
		if ri.Item.ID == "unaccounted" {
			continue
		}
		if !ri.Item.Actionable() {
			seenImmovable = true
			continue
		}
		if seenImmovable {
			t.Fatalf("actionable item %q sorted after an immovable one", ri.Item.ID)
		}
	}

	// The residual is pinned last: it is the one row nobody can act on.
	if rep.Unaccounted != nil && ranked[len(ranked)-1].Item.ID != rep.Unaccounted.ID {
		t.Fatalf("last row = %q, want the residual %q", ranked[len(ranked)-1].Item.ID, rep.Unaccounted.ID)
	}
}

func TestContextPagerRankingRespectsLoadState(t *testing.T) {
	rep := &ctxinspect.Report{
		Categories: []ctxinspect.Category{{
			Name: "skills",
			Items: []ctxinspect.Item{
				{
					ID:     "available",
					Label:  "available",
					Load:   ctxinspect.AvailableCost(ctxinspect.Estimated(50000, "chars/4")),
					Lever:  ctxinspect.DirLever("/skills/available", "on disk"),
					Origin: ctxinspect.OriginUserConfig,
				},
				{
					ID:     "loaded",
					Label:  "loaded",
					Load:   ctxinspect.LoadedNowCost(ctxinspect.Estimated(10, "chars/4")),
					Lever:  ctxinspect.DirLever("/skills/loaded", "in the prefix"),
					Origin: ctxinspect.OriginUserConfig,
				},
			},
		}},
	}
	ranked := contextRankAll(rep)
	if ranked[0].Item.ID != "loaded" {
		t.Fatalf("first row = %q; an item that costs nothing today must not head a list about what is costing you", ranked[0].Item.ID)
	}
}

func TestContextPagerDrillDownAndBack(t *testing.T) {
	p := newContextPagerForTest(t, buildContextTestReport())

	if p.Depth() != 0 {
		t.Fatalf("Depth = %d, want 0 at a tab root", p.Depth())
	}
	if !p.Descend() {
		t.Fatal("Enter on the first category must descend")
	}
	if p.Depth() != 1 {
		t.Fatalf("Depth = %d after descending into a category, want 1", p.Depth())
	}
	if !p.Descend() {
		t.Fatal("Enter on an item must open its content")
	}
	if p.Depth() != 2 {
		t.Fatalf("Depth = %d after descending into an item, want 2", p.Depth())
	}
	out := ansi.Strip(p.View())
	if !strings.Contains(out, "--- content") {
		t.Fatalf("level 3 must render the item's bytes:\n%s", out)
	}

	if !p.Ascend() || p.Depth() != 1 {
		t.Fatalf("Esc must pop one level, Depth = %d", p.Depth())
	}
	if !p.Ascend() || p.Depth() != 0 {
		t.Fatalf("Esc must pop back to the root, Depth = %d", p.Depth())
	}
	if p.Ascend() {
		t.Fatal("Ascend at a tab root must report false so the caller closes the overlay")
	}
}

func TestContextPagerTabsKeepIndependentDrillState(t *testing.T) {
	p := newContextPagerForTest(t, buildContextTestReport())

	p.Descend() // overview -> category items
	if p.Depth() != 1 {
		t.Fatalf("overview depth = %d, want 1", p.Depth())
	}

	p.SetTab(contextPagerTabBreakdown)
	if p.Depth() != 0 {
		t.Fatalf("breakdown depth = %d, want its own root", p.Depth())
	}
	p.Descend()
	if p.Depth() != 1 {
		t.Fatalf("breakdown depth = %d after descending, want 1", p.Depth())
	}

	p.SetTab(contextPagerTabOverview)
	if p.Depth() != 1 {
		t.Fatalf("overview depth = %d after returning, want its preserved 1", p.Depth())
	}

	p.SetTab(contextPagerTabVerify)
	if p.Descend() {
		t.Fatal("the verify tab has nothing to descend into")
	}
}

func TestContextPagerTabCycling(t *testing.T) {
	p := newContextPagerForTest(t, buildContextTestReport())
	for _, want := range []int{contextPagerTabBreakdown, contextPagerTabVerify, contextPagerTabOverview} {
		p.NextTab()
		if p.Tab() != want {
			t.Fatalf("NextTab -> %d, want %d", p.Tab(), want)
		}
	}
	p.PrevTab()
	if p.Tab() != contextPagerTabVerify {
		t.Fatalf("PrevTab -> %d, want %d", p.Tab(), contextPagerTabVerify)
	}
	p.SetTab(99)
	if p.Tab() != contextPagerTabVerify {
		t.Fatal("an out-of-range tab index must be ignored")
	}
}

func TestContextPagerScrollingStaysInBounds(t *testing.T) {
	p := newContextPagerForTest(t, buildContextTestReport())
	p.SetTab(contextPagerTabVerify) // no selectable rows: pure scrolling

	p.ScrollUp(1000)
	if got := p.current().offset; got != 0 {
		t.Fatalf("offset = %d after scrolling past the top, want 0", got)
	}
	p.ScrollDown(100000)
	if got, max := p.current().offset, p.maxOffset(); got != max {
		t.Fatalf("offset = %d after scrolling past the end, want maxOffset %d", got, max)
	}
	p.Top()
	if got := p.current().offset; got != 0 {
		t.Fatalf("Top left offset at %d", got)
	}
	p.Bottom()
	if got, max := p.current().offset, p.maxOffset(); got != max {
		t.Fatalf("Bottom left offset at %d, want %d", got, max)
	}
}

func TestContextPagerCursorStaysWithinRows(t *testing.T) {
	p := newContextPagerForTest(t, buildContextTestReport())
	rows := p.rowCount()
	if rows == 0 {
		t.Fatal("the overview must have selectable rows")
	}
	p.MoveCursor(-50)
	if got := p.current().cursor; got != 0 {
		t.Fatalf("cursor = %d after moving past the top, want 0", got)
	}
	p.MoveCursor(500)
	if got := p.current().cursor; got != rows-1 {
		t.Fatalf("cursor = %d after moving past the end, want %d", got, rows-1)
	}
}

func TestContextPagerResizeReclamps(t *testing.T) {
	p := newContextPagerForTest(t, buildContextTestReport())
	p.SetTab(contextPagerTabVerify)
	p.Bottom()
	p.SetSize(120, 200) // a much taller terminal shrinks maxOffset
	if got, max := p.current().offset, p.maxOffset(); got > max {
		t.Fatalf("offset %d exceeds maxOffset %d after a resize", got, max)
	}
	// A degenerate size must not panic or produce a negative body height.
	p.SetSize(1, 1)
	if p.bodyHeight() < 1 {
		t.Fatalf("bodyHeight = %d, want at least 1", p.bodyHeight())
	}
	_ = p.View()
}

func TestContextPagerLeverSelection(t *testing.T) {
	p := newContextPagerForTest(t, buildContextTestReport())
	p.SetTab(contextPagerTabBreakdown)

	lever, ok := p.SelectedLever()
	if !ok {
		t.Fatal("the top breakdown row is actionable and must expose a lever")
	}
	if lever.Payload() != "/home/u/.claude/CLAUDE.md" {
		t.Fatalf("lever payload = %q", lever.Payload())
	}

	// Walk to the immovable system-prompt segment: it must refuse a copy
	// rather than hand back an empty payload that looks like a success.
	items := p.current().items
	target := -1
	for i, ri := range items {
		if !ri.Item.Actionable() {
			target = i
			break
		}
	}
	if target < 0 {
		t.Fatal("the fixture must contain an immovable item")
	}
	p.current().cursor = target
	if _, ok := p.SelectedLever(); ok {
		t.Fatal("an immovable item must not report a copyable lever")
	}
}

func TestContextPagerCategoryRowsAreNotItems(t *testing.T) {
	p := newContextPagerForTest(t, buildContextTestReport())
	if _, ok := p.SelectedItem(); ok {
		t.Fatal("a category row is not an item and must not be reported as one")
	}
}

func TestContextPagerOverviewRendersProvenanceAndSelfCheck(t *testing.T) {
	rep := buildContextTestReport()
	p := newContextPagerForTest(t, rep)
	out := ansi.Strip(p.View())

	for _, want := range []string{
		"OBSERVED",               // basis is always visible
		"fixed startup overhead", // the gauge
		"memory files",           // an adapter-declared category
		"RECON/~est",             // both provenance axes, collapsed by Badge
		"history:",               // the single orientation line
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("overview is missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, strings.ToUpper(rep.Reconciliation.Status.String())) {
		t.Fatalf("the reconciliation verdict must be visible:\n%s", out)
	}
}

func TestContextPagerVerdictAndCalibrationVisibleOnEveryScreen(t *testing.T) {
	rep := buildContextTestReport()
	p := newContextPagerForTest(t, rep)
	verdict := strings.ToUpper(rep.Reconciliation.Status.String())

	check := func(where string) {
		t.Helper()
		out := ansi.Strip(p.View())
		if !strings.Contains(out, verdict) {
			t.Fatalf("%s: the reconciliation verdict must stay visible:\n%s", where, out)
		}
		if !strings.Contains(out, "estimate") && !strings.Contains(out, "no estimated figures") {
			t.Fatalf("%s: the estimator's error bound must stay visible:\n%s", where, out)
		}
	}

	check("overview")
	p.Descend()
	check("level 2")
	p.Descend()
	check("level 3")
	p.SetTab(contextPagerTabBreakdown)
	check("breakdown")
	p.SetTab(contextPagerTabVerify)
	check("verify")
}

func TestContextPagerUnknownTokensRenderAsDashNeverZero(t *testing.T) {
	if got := contextTokenCount(ctxinspect.UnknownTokens("no figure")); got != "—" {
		t.Fatalf("an unknown count rendered as %q, want an em dash", got)
	}
	if got := contextTotalTokens(0, false); got != "—" {
		t.Fatalf("a zero lower bound rendered as %q; ≥0 is not a bound", got)
	}
	if got := contextTotalTokens(1200, false); got != "≥1.2k" {
		t.Fatalf("an incomplete total rendered as %q, want ≥1.2k", got)
	}
	if got := contextTotalTokens(1200, true); got != "1.2k" {
		t.Fatalf("a complete total rendered as %q, want 1.2k", got)
	}
}

func TestContextPagerTokenAmountFormatting(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1.0k"},
		{125213, "125.2k"},
		{1_000_000, "1.0M"},
		{-4200, "-4.2k"}, // a negative residual keeps its sign; it is a reported bug
	}
	for _, tc := range cases {
		if got := contextTokenAmount(tc.in); got != tc.want {
			t.Errorf("contextTokenAmount(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestContextPagerEstimateMarkers(t *testing.T) {
	cases := []struct {
		name  string
		count ctxinspect.TokenCount
		want  string
	}{
		{"measured", ctxinspect.Measured(20000, "provider"), "20.0k"},
		{"estimated", ctxinspect.Estimated(4000, "chars/4"), "~4.0k"},
		{"residual", ctxinspect.ResidualTokens(1500, "anchor − attributed"), "~1.5k"},
		{"unknown", ctxinspect.UnknownTokens("nothing to read"), "—"},
	}
	for _, tc := range cases {
		if got := contextTokenCount(tc.count); got != tc.want {
			t.Errorf("%s rendered as %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestContextPagerUnsupportedHarnessIsAPopulatedScreen(t *testing.T) {
	p := NewContextPager()
	p.Show("demo", "session-2", "aider", 120, 30)
	p.SetReport(buildContextUnsupportedReport(), nil)
	out := ansi.Strip(p.View())

	if !strings.Contains(out, "token accounting unsupported for aider") {
		t.Fatalf("an unmeasurable harness must say so:\n%s", out)
	}
	if !strings.Contains(out, "instruction files") {
		t.Fatalf("an unmeasurable harness must still list its inventory:\n%s", out)
	}
	if strings.Contains(out, "(0.0%)") {
		t.Fatalf("no percentage may be shown without a window and a known total:\n%s", out)
	}
}

func TestContextPagerUnsupportedBannerNotShownWhenMeasurable(t *testing.T) {
	if _, ok := contextUnsupportedBanner(buildContextTestReport(), "claude"); ok {
		t.Fatal("a harness with a measured anchor must not print the unsupported banner")
	}
	if _, ok := contextUnsupportedBanner(buildContextUnsupportedReport(), "aider"); !ok {
		t.Fatal("a harness with no token accounting must print the unsupported banner")
	}
}

func TestContextPagerViewNeverExceedsTerminalWidth(t *testing.T) {
	rep := buildContextTestReport()
	for _, width := range []int{20, 40, 80, 200} {
		p := NewContextPager()
		p.Show("a-very-long-session-title-that-will-not-fit-anywhere", "session-1", "claude", width, 24)
		p.SetReport(rep, []string{"a resolution warning long enough to need truncation on a narrow terminal"})
		for tab := 0; tab < contextPagerTabCount; tab++ {
			p.SetTab(tab)
			for _, line := range strings.Split(p.View(), "\n") {
				if w := cellWidth(ansi.Strip(line)); w > width {
					t.Fatalf("width %d tab %d: line of %d cells overflows: %q", width, tab, w, ansi.Strip(line))
				}
			}
		}
	}
}

func TestContextPagerViewResetsSGRPerLine(t *testing.T) {
	// Captured content can carry its own escape codes; every emitted line must
	// end the SGR state so a colour cannot bleed into the chrome.
	rep := buildContextTestReport()
	rep.Categories[0].Items[0].Content = ctxinspect.CapturedContent(
		ctxinspect.KindProse, "\x1b[31mred text that never closes")
	p := newContextPagerForTest(t, rep)
	p.Descend()
	p.Descend()
	out := p.View()
	if !strings.Contains(out, "\x1b[0m") {
		t.Fatal("the body must emit an SGR reset so captured colour cannot bleed")
	}
}

func TestContextPagerBodyMemoIsInvalidatedByNewReports(t *testing.T) {
	p := newContextPagerForTest(t, buildContextTestReport())
	first := p.lineCount()
	if first == 0 {
		t.Fatal("the overview rendered nothing")
	}
	p.SetReport(buildContextUnsupportedReport(), nil)
	if second := p.lineCount(); second == first && second > 20 {
		t.Fatal("the rendered-body memo survived a new report")
	}
	p.SetError("gone")
	if p.lineCount() != 0 {
		t.Fatal("an error state must render no body lines")
	}
}

func TestContextPagerHideReleasesTheReport(t *testing.T) {
	p := newContextPagerForTest(t, buildContextTestReport())
	p.Hide()
	if p.IsVisible() || p.Report() != nil || p.SessionID() != "" {
		t.Fatal("Hide must close the pager and release its report")
	}
	if p.View() != "" {
		t.Fatal("a hidden pager renders nothing")
	}
}

func TestContextPagerSegmentsAreSelectableAtLevelThree(t *testing.T) {
	p := newContextPagerForTest(t, buildContextTestReport())
	p.SetTab(contextPagerTabBreakdown)

	// Find the segmented system-prompt item in the flat ranking.
	items := p.current().items
	target := -1
	for i, ri := range items {
		if ri.Item.ID == "system:base" {
			target = i
			break
		}
	}
	if target < 0 {
		t.Fatal("the fixture must contain the segmented system-prompt item")
	}
	p.current().cursor = target
	if !p.Descend() {
		t.Fatal("Enter must open the segmented item")
	}
	if p.rowCount() != 2 {
		t.Fatalf("rowCount = %d at level 3, want one row per segment", p.rowCount())
	}
	out := ansi.Strip(p.View())
	if !strings.Contains(out, "segments") {
		t.Fatalf("level 3 must show per-segment costs:\n%s", out)
	}
	if !p.Descend() {
		t.Fatal("Enter on a segment must open that segment")
	}
	if p.Depth() != 2 {
		t.Fatalf("Depth = %d after opening a segment, want 2", p.Depth())
	}
}

func TestContextPagerNilReceiverIsSafe(t *testing.T) {
	var p *ContextPager
	if p.IsVisible() || p.SessionID() != "" || p.Report() != nil || p.View() != "" {
		t.Fatal("a nil pager must answer safely")
	}
	// None of these may panic on a nil receiver: the overlay is reachable from
	// several call sites and a nil field must degrade, not crash.
	p.SetSize(10, 10)
	p.Show("a", "b", "c", 10, 10)
	p.Hide()
	p.SetReport(nil, nil)
	p.SetError("x")
	p.SetStatus("x")
	p.ClearStatus()
	p.SetLoading()
	p.SetTab(0)
	p.MoveCursor(1)
	p.ScrollUp(1)
	p.ScrollDown(1)
	p.PageUp()
	p.PageDown()
	p.Top()
	p.Bottom()
	if p.Descend() || p.Ascend() {
		t.Fatal("a nil pager cannot navigate")
	}
}

func TestContextTableAlignsByCellWidth(t *testing.T) {
	// The em dash and the lock glyph occupy a different number of terminal
	// cells from their rune count; padding by rune count skews every column
	// after them.
	rows := [][]string{
		{"TOKENS", "ITEM"},
		{"—", "unpriced"},
		{"125.2k", "memory"},
	}
	lines := contextTable(rows, "  ")
	if len(lines) != 3 {
		t.Fatalf("contextTable returned %d lines, want 3", len(lines))
	}
	col := -1
	for _, line := range lines {
		idx := cellWidth(line[:strings.Index(line, strings.Fields(line)[1])])
		if col < 0 {
			col = idx
			continue
		}
		if idx != col {
			t.Fatalf("second column starts at cell %d, want %d (line %q)", idx, col, line)
		}
	}
	if strings.HasSuffix(lines[1], " ") {
		t.Fatal("the final column must not be padded")
	}
}

func TestContextLeverLineNamesTheAction(t *testing.T) {
	cases := []struct {
		name  string
		lever ctxinspect.Lever
		want  string
	}{
		{"file", ctxinspect.FileLever("/repo/CLAUDE.md", "in the project root"), "edit /repo/CLAUDE.md"},
		{"dir", ctxinspect.DirLever("/skills/x", "unused"), "delete directory /skills/x"},
		{"command", ctxinspect.CommandLever("agent-deck mcp detach s m", "attached by agent-deck"), "run: agent-deck mcp detach s m"},
		{"immovable", ctxinspect.ImmovableLever("shipped in the binary"), "immovable"},
	}
	for _, tc := range cases {
		got := contextLeverLine(tc.lever)
		if !strings.HasPrefix(got, tc.want) {
			t.Errorf("%s lever rendered as %q, want it to start with %q", tc.name, got, tc.want)
		}
	}
}

func TestContextWindowLineAlwaysNamesItsSource(t *testing.T) {
	got := contextWindowLine(ctxinspect.WindowInfo{Tokens: 200000, Source: ctxinspect.WindowModelDefault, Detail: "claude-test"})
	if !strings.Contains(got, "model-default") || !strings.Contains(got, "claude-test") {
		t.Fatalf("the window's source must be named, got %q", got)
	}
}

// The two surfaces over one report must answer the same question the same way.
// They did not: asked about a session whose window could not be established,
// the CLI printed a sentence naming the reason and the fix, and this pager
// printed the bare word "unknown".
func TestContextWindowLineIsNeverADeadEnd(t *testing.T) {
	got := contextWindowLine(ctxinspect.WindowInfo{})
	if got == "unknown" {
		t.Fatal("the bare word 'unknown' is a dead end: it cannot tell a reader whether the feature is broken, the model unsupported, or the fix one variable away")
	}
	if !strings.Contains(got, ctxtext.WindowEnvVar) {
		t.Fatalf("an unknown window must carry the one line that fixes it, got %q", got)
	}
	if got != ctxtext.WindowLine(ctxinspect.WindowInfo{}) {
		t.Fatalf("the pager must render the window through the shared formatter, got %q", got)
	}
}

// An unknown window must not be drawn with the glyph a working gauge uses at
// 0%: identical pixels for "empty" and "unmeasurable" is a lie told in
// punctuation. The remedy travels on the gauge line itself, and the reason
// follows immediately underneath rather than in the caveat block far below.
func TestContextGaugeUnknownWindowKeepsABarAndExplainsItself(t *testing.T) {
	rep := buildContextTestReport()
	rep.Window = ctxinspect.WindowInfo{Source: ctxinspect.WindowUnknown, Detail: "no context-window size is known for model \"claude-test-9\""}

	fixed, complete := rep.FixedTotal()
	gauge := contextGaugeLine(rep, fixed, complete)
	if !strings.Contains(gauge, "/ ?") {
		t.Fatalf("the gauge must show the absolute total against a missing denominator, got %q", gauge)
	}
	if strings.Contains(gauge, contextGaugeBar(0, contextGaugeWidth)) {
		t.Fatalf("an unknown window must not render identically to 0%%, got %q", gauge)
	}

	// The reason and the remedy live on the wrapped lines directly beneath,
	// not hung off the gauge's right edge where this pane truncates.
	lines := contextWindowUnknownLines(rep.Window, 80)
	joined := strings.Join(lines, " ")
	if !strings.Contains(joined, "claude-test-9") {
		t.Fatalf("the reason must be stated at the point of confusion, got %q", joined)
	}
	if !strings.Contains(joined, ctxtext.WindowEnvVar) {
		t.Fatalf("the remedy must be stated at the point of confusion, got %q", joined)
	}
	for _, l := range lines {
		if cellWidth(l)+2 > 80 {
			t.Fatalf("the explanation must fit the pane, got a %d-cell line: %q", cellWidth(l), l)
		}
	}
}

// A percentage computed from a window nothing measured must not read like one
// that was. The mark and the qualifier travel with the figure.
func TestContextGaugeMarksAnAssumedWindow(t *testing.T) {
	rep := buildContextTestReport()
	rep.Window = ctxinspect.WindowInfo{Tokens: 1_000_000, Source: ctxinspect.WindowModelFamily, Detail: "assumed from the claude-test family"}

	fixed, complete := rep.FixedTotal()
	gauge := contextGaugeLine(rep, fixed, complete)
	if !strings.Contains(gauge, "≈") {
		t.Fatalf("a percentage over an assumed denominator must be marked, got %q", gauge)
	}
	joined := strings.Join(contextWindowUnknownLines(rep.Window, 80), " ")
	if !strings.Contains(joined, "assumed") || !strings.Contains(joined, ctxtext.WindowEnvVar) {
		t.Fatalf("the mark must be explained beneath the gauge, got %q", joined)
	}
}
