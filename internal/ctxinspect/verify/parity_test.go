package verify

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
	"github.com/asheshgoplani/agent-deck/internal/ctxinspect/claude"
)

// buildParityReport returns a Claude-shaped report whose figures line up with
// claudeContextPane: memory 4,500, agents 1,600, system tools + MCP + residual
// summing to the panel's system prompt + system tools + MCP tools.
func buildParityReport(t *testing.T) *ctxinspect.Report {
	t.Helper()

	cat := func(name, title string, items ...ctxinspect.Item) ctxinspect.Category {
		return ctxinspect.Category{Name: name, Title: title, Items: items}
	}
	item := func(id, label string, tokens int) ctxinspect.Item {
		return ctxinspect.Item{
			ID:      id,
			Label:   label,
			Content: ctxinspect.ReconstructedContent(ctxinspect.KindMarkdown, strings.Repeat("x", tokens*4), "rebuilt for the test"),
			Load:    ctxinspect.LoadedNowCost(ctxinspect.Estimated(tokens, "test fixture")),
			Origin:  ctxinspect.OriginProject,
			Lever:   ctxinspect.FileLever("/tmp/example/CLAUDE.md", "test fixture"),
		}
	}

	rep := &ctxinspect.Report{
		Harness: "claude",
		Adapter: "claude",
		Model:   "claude-opus-4-7",
		Window:  ctxinspect.WindowInfo{Tokens: 200000, Source: ctxinspect.WindowModelDefault, Detail: "test"},
		Basis:   ctxinspect.BasisObserved,
		Anchor: &ctxinspect.Anchor{
			Tokens: ctxinspect.Measured(23000, "usage.input+cache_creation+cache_read"),
			Source: "first assistant turn",
		},
		Categories: []ctxinspect.Category{
			cat(claude.CategorySystemTools, "system tools", item("tools:0", "deferred tool names", 900)),
			cat(ctxinspect.CategoryMCP, "MCP instructions", item("mcp:0", "telegram", 600)),
			cat(claude.CategoryMemory, "memory files", item("memory:0", "CLAUDE.md", 4500)),
			cat(claude.CategoryAgents, "agents", item("agents:0", "agent catalogue", 1600)),
		},
		History: &ctxinspect.HistoryLine{
			Tokens: ctxinspect.Measured(93000, "last turn's prompt-side usage"),
			Turns:  12,
			Note:   "orientation only",
		},
	}
	rep.Reconcile()
	return rep
}

func claudeSpec(t *testing.T) Spec {
	t.Helper()
	spec, ok := SpecForAdapter("claude")
	if !ok {
		t.Fatal("the claude adapter must have a verification spec")
	}
	return spec
}

func rowByGroup(t *testing.T, p *Parity, name string) Row {
	t.Helper()
	for _, r := range p.Rows {
		if r.Group == name {
			return r
		}
	}
	t.Fatalf("no parity row named %q; have %v", name, groupNames(p))
	return Row{}
}

func groupNames(p *Parity) []string {
	out := make([]string, 0, len(p.Rows))
	for _, r := range p.Rows {
		out = append(out, r.Group)
	}
	return out
}

func TestCompareMatchesDirectlyComparableGroups(t *testing.T) {
	rep := buildParityReport(t)
	h, err := ParseClaudeContext(claudeContextPane)
	if err != nil {
		t.Fatalf("ParseClaudeContext: %v", err)
	}

	p := Compare(rep, h, claudeSpec(t), DefaultTolerance())

	memory := rowByGroup(t, p, "memory files")
	if memory.Verdict != VerdictMatch {
		t.Errorf("memory files: verdict = %s (harness %d, ours %d, allowed %d)", memory.Verdict, memory.Harness, memory.Ours, memory.Allowed)
	}
	agents := rowByGroup(t, p, "agents")
	if agents.Verdict != VerdictMatch {
		t.Errorf("agents: verdict = %s (harness %d, ours %d)", agents.Verdict, agents.Harness, agents.Ours)
	}
	if !p.WindowAgrees {
		t.Errorf("window should agree: %s", p.WindowNote)
	}
}

// TestCompareGroupsHarnessInternalsWithTheResidual is the design's core claim:
// the two accountings slice the harness's own overhead differently, and only
// the sum is equivalent.
func TestCompareGroupsHarnessInternalsWithTheResidual(t *testing.T) {
	rep := buildParityReport(t)
	h, err := ParseClaudeContext(claudeContextPane)
	if err != nil {
		t.Fatalf("ParseClaudeContext: %v", err)
	}

	row := rowByGroup(t, Compare(rep, h, claudeSpec(t), DefaultTolerance()), "harness internals")
	if wantHarness := 3200 + 12400 + 1500; row.Harness != wantHarness {
		t.Errorf("harness side = %d, want %d (system prompt + system tools + MCP tools)", row.Harness, wantHarness)
	}
	residual, _ := rep.Unaccounted.Load.Actual.Value()
	if wantOurs := 900 + 600 + residual; row.Ours != wantOurs {
		t.Errorf("agent-deck side = %d, want %d (tool names + MCP instructions + residual)", row.Ours, wantOurs)
	}
}

func TestCompareFlagsDriftBeyondTolerance(t *testing.T) {
	rep := buildParityReport(t)
	// Restate memory as a quarter of what the panel reports.
	for i, c := range rep.Categories {
		if c.Name != claude.CategoryMemory {
			continue
		}
		rep.Categories[i].Items[0].Load = ctxinspect.LoadedNowCost(ctxinspect.Estimated(1100, "test fixture"))
	}
	rep.Reconcile()

	h, err := ParseClaudeContext(claudeContextPane)
	if err != nil {
		t.Fatalf("ParseClaudeContext: %v", err)
	}
	p := Compare(rep, h, claudeSpec(t), DefaultTolerance())

	row := rowByGroup(t, p, "memory files")
	if row.Verdict != VerdictDrift {
		t.Fatalf("verdict = %s, want DRIFT (harness %d, ours %d, allowed %d)", row.Verdict, row.Harness, row.Ours, row.Allowed)
	}
	if p.Status != StatusDrift {
		t.Fatalf("overall status = %s, want drift", p.Status)
	}
	if len(p.Drifted()) != 1 {
		t.Fatalf("Drifted() = %d rows, want 1", len(p.Drifted()))
	}
}

// TestCompareToleranceAbsorbsPrintedRounding: a panel that prints "4.5k" has
// already thrown away 50 tokens, and that must never be reported as our
// disagreement.
func TestCompareToleranceAbsorbsPrintedRounding(t *testing.T) {
	tol := Tolerance{AbsTokens: 0, Pct: 0}
	if got := tol.Allowed(4500, 50); got != 50 {
		t.Fatalf("Allowed = %d, want the printed slack of 50", got)
	}
	if got := DefaultTolerance().Allowed(100000, 500); got != 10500 {
		t.Fatalf("Allowed = %d, want 10%% of 100000 plus 500 slack", got)
	}
	if got := DefaultTolerance().Allowed(1000, 0); got != 500 {
		t.Fatalf("Allowed = %d, want the 500-token floor to win at small figures", got)
	}
}

// TestCompareRefusesToCallALowerBoundAMatch: an agreement reached by ignoring
// an unpriced item is not an agreement, because the missing number could
// overturn it.
func TestCompareRefusesToCallALowerBoundAMatch(t *testing.T) {
	rep := buildParityReport(t)
	for i, c := range rep.Categories {
		if c.Name != claude.CategoryMemory {
			continue
		}
		rep.Categories[i].Items = append(rep.Categories[i].Items, ctxinspect.Item{
			ID:      "memory:unpriced",
			Label:   "an unreadable memory file",
			Content: ctxinspect.AbsentContent("unreadable"),
			Load:    ctxinspect.Load{State: ctxinspect.LoadedNow, Actual: ctxinspect.UnknownTokens("file could not be read")},
			Origin:  ctxinspect.OriginProject,
			Lever:   ctxinspect.FileLever("/tmp/example/CLAUDE.md", "test fixture"),
		})
	}
	rep.Reconcile()

	h, err := ParseClaudeContext(claudeContextPane)
	if err != nil {
		t.Fatalf("ParseClaudeContext: %v", err)
	}
	row := rowByGroup(t, Compare(rep, h, claudeSpec(t), DefaultTolerance()), "memory files")
	if row.Verdict != VerdictOursUnknown {
		t.Fatalf("verdict = %s, want ours-unknown for a lower-bound side", row.Verdict)
	}
	if row.OursComplete {
		t.Error("the row must be marked incomplete")
	}
}

// TestCompareReportsHarnessSilenceRatherThanAgreement: a version whose panel
// omits a row must produce "harness silent", never a match against nothing.
func TestCompareReportsHarnessSilenceRatherThanAgreement(t *testing.T) {
	rep := buildParityReport(t)
	rep.Categories = append(rep.Categories, ctxinspect.Category{
		Name:  claude.CategorySkills,
		Title: "skills",
		Items: []ctxinspect.Item{{
			ID:      "skills:0",
			Label:   "skill catalogue",
			Content: ctxinspect.CapturedContent(ctxinspect.KindListing, "skills"),
			Load:    ctxinspect.LoadedNowCost(ctxinspect.Estimated(4500, "test fixture")),
			Origin:  ctxinspect.OriginUserConfig,
			Lever:   ctxinspect.DirLever("/tmp/example/skills", "test fixture"),
		}},
	})
	rep.Reconcile()

	h, err := ParseClaudeContext(claudeContextPane) // this panel has no skills row
	if err != nil {
		t.Fatalf("ParseClaudeContext: %v", err)
	}
	row := rowByGroup(t, Compare(rep, h, claudeSpec(t), DefaultTolerance()), "skills")
	if row.Verdict != VerdictHarnessSilent {
		t.Fatalf("verdict = %s, want harness-silent", row.Verdict)
	}
	if row.HarnessKnown {
		t.Error("the harness side must be marked unknown, not zero")
	}
}

// TestCompareMessagesRowIsInformational: agent-deck reports one orientation
// figure for the conversation and does not attribute it, so the row is printed
// and never graded.
func TestCompareMessagesRowIsInformational(t *testing.T) {
	rep := buildParityReport(t)
	h, err := ParseClaudeContext(claudeContextPane)
	if err != nil {
		t.Fatalf("ParseClaudeContext: %v", err)
	}
	row := rowByGroup(t, Compare(rep, h, claudeSpec(t), DefaultTolerance()), "messages")
	if row.Verdict != VerdictInformational {
		t.Fatalf("verdict = %s, want informational", row.Verdict)
	}
}

// TestCompareSurfacesUnmappedPanelRows: a row nothing claims must be listed, so
// a new panel row is visible rather than silently excluded from every sum.
func TestCompareSurfacesUnmappedPanelRows(t *testing.T) {
	rep := buildParityReport(t)
	h, err := ParseClaudeContext(claudeContextPane)
	if err != nil {
		t.Fatalf("ParseClaudeContext: %v", err)
	}
	h.Figures = append(h.Figures, Figure{Label: "background tasks", Raw: "Background tasks", Tokens: 2000})

	p := Compare(rep, h, claudeSpec(t), DefaultTolerance())
	if len(p.Unmapped) != 1 || !strings.Contains(p.Unmapped[0], "Background tasks") {
		t.Fatalf("Unmapped = %v, want the unclaimed row", p.Unmapped)
	}
	for _, u := range p.Unmapped {
		if strings.Contains(u, "Free space") || strings.Contains(u, "Autocompact") {
			t.Errorf("a deliberately excluded row must not be reported as unmapped: %q", u)
		}
	}
}

// TestCompareFlagsAWindowMismatch: a different denominator makes every
// percentage on one side wrong, so it is checked rather than assumed.
func TestCompareFlagsAWindowMismatch(t *testing.T) {
	rep := buildParityReport(t)
	rep.Window = ctxinspect.WindowInfo{Tokens: 1000000, Source: ctxinspect.WindowSettings, Detail: "test"}
	h, err := ParseClaudeContext(claudeContextPane)
	if err != nil {
		t.Fatalf("ParseClaudeContext: %v", err)
	}
	p := Compare(rep, h, claudeSpec(t), DefaultTolerance())
	if p.WindowAgrees {
		t.Fatal("a 200k panel and a 1M report must not be reported as agreeing")
	}
	if !strings.Contains(p.WindowNote, "WINDOW MISMATCH") {
		t.Fatalf("WindowNote = %q, want an explicit mismatch", p.WindowNote)
	}
}

func TestCompareWithNoComparableGroupIsIndeterminate(t *testing.T) {
	rep := buildParityReport(t)
	h := &HarnessReport{Harness: "claude", Command: ClaudeCommand}
	p := Compare(rep, h, claudeSpec(t), DefaultTolerance())
	if p.Status != StatusIndeterminate {
		t.Fatalf("status = %s, want indeterminate: nothing was graded", p.Status)
	}
}

func TestSpecForAdapter(t *testing.T) {
	for _, name := range []string{"claude", "Claude", " codex "} {
		if _, ok := SpecForAdapter(name); !ok {
			t.Errorf("SpecForAdapter(%q) must resolve", name)
		}
	}
	if _, ok := SpecForAdapter("generic"); ok {
		t.Error("the generic adapter has no ground-truth command and must not claim one")
	}
	err := ErrUnverifiable{Adapter: "generic"}
	if !strings.Contains(err.Error(), "generic") {
		t.Errorf("ErrUnverifiable must name the adapter: %q", err.Error())
	}
}
