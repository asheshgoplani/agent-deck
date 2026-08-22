package ctxinspect

import (
	"strings"
	"testing"
)

// pricedItem builds a valid, actionable, loaded item costing an estimated
// number of tokens. Tests that care about one invariant start from something
// that violates none of the others.
func pricedItem(id string, tokens int) Item {
	return Item{
		ID:      id,
		Label:   id,
		Content: ReconstructedContent(KindMarkdown, "body of "+id, "rebuilt from disk"),
		Load:    LoadedNowCost(Estimated(tokens, "chars/4")),
		Origin:  OriginProject,
		Lever:   FileLever("/tmp/"+id+".md", "project file"),
	}
}

// anchoredReport builds a report with a measured anchor and one category.
func anchoredReport(anchor int, items ...Item) *Report {
	return &Report{
		Harness: "claude",
		Basis:   BasisObserved,
		Anchor:  &Anchor{Tokens: Measured(anchor, "first assistant turn usage"), Source: "usage"},
		Categories: []Category{{
			Name:  "memory-files",
			Title: "memory files",
			Items: items,
		}},
	}
}

func TestReconcileComputesResidualAgainstAnchor(t *testing.T) {
	r := anchoredReport(1000, pricedItem("a", 300), pricedItem("b", 200))
	r.Reconcile()

	if r.Reconciliation.Status != ReconOK {
		t.Fatalf("status = %s, want ok", r.Reconciliation.Status)
	}
	got, ok := r.Reconciliation.Unaccounted.Value()
	if !ok || got != 500 {
		t.Fatalf("residual = %d (known=%v), want 500", got, ok)
	}
	if r.Unaccounted == nil {
		t.Fatal("the residual must be a first-class item with its own row, never a footnote")
	}
	if r.Unaccounted.Content.Prov != TextAbsent {
		t.Fatal("the residual has no text by definition and must be marked absent")
	}
	if r.Unaccounted.Load.Actual.Prov() != TokenResidual {
		t.Fatalf("residual provenance = %s, want residual", r.Unaccounted.Load.Actual.Prov())
	}
}

func TestReconcileNeverClampsANegativeResidual(t *testing.T) {
	r := anchoredReport(1000, pricedItem("a", 900), pricedItem("b", 400))
	r.Reconcile()

	if r.Reconciliation.Status != ReconFailed {
		t.Fatalf("status = %s, want failed: attributing more than the provider measured is a bug and must be reported", r.Reconciliation.Status)
	}
	got, ok := r.Reconciliation.Unaccounted.Value()
	if !ok {
		t.Fatal("the over-attribution must stay known so the number is visible")
	}
	if got != -300 {
		t.Fatalf("residual = %d, want -300 (clamping to 0 is how a plausible lie gets shipped)", got)
	}
	if !strings.Contains(r.Reconciliation.Message, "RECONCILIATION FAILED") {
		t.Fatalf("message %q must name the failure", r.Reconciliation.Message)
	}
	if r.Reconciliation.OK() {
		t.Fatal("a failed reconciliation must not report OK")
	}
}

func TestValidateAddsABugCaveatForAFailedReconciliation(t *testing.T) {
	r := anchoredReport(100, pricedItem("a", 500))
	r.Reconcile()
	_ = r.Validate()

	var found bool
	for _, c := range r.Caveats {
		if c.Code == "reconciliation-failed" {
			found = true
			if c.Severity != SeverityBug {
				t.Fatalf("severity = %s, want bug", c.Severity)
			}
		}
	}
	if !found {
		t.Fatal("a failed reconciliation must never be silent")
	}

	// Finalising twice must not duplicate the caveat.
	r.Reconcile()
	_ = r.Validate()
	var count int
	for _, c := range r.Caveats {
		if c.Code == "reconciliation-failed" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("caveat recorded %d times, want 1: finalisation must be idempotent", count)
	}
}

func TestReconcileWithoutAnAnchorReportsUnknownNotZero(t *testing.T) {
	r := anchoredReport(0, pricedItem("a", 300))
	r.Anchor = nil
	r.Basis = BasisProjected
	r.Reconcile()

	if r.Reconciliation.Status != ReconNoAnchor {
		t.Fatalf("status = %s, want no-anchor", r.Reconciliation.Status)
	}
	if r.Reconciliation.Unaccounted.Known() {
		t.Fatal("with no anchor the unattributed remainder is unknowable and must not be reported as 0")
	}
	if _, complete := r.FixedTotal(); complete {
		t.Fatal("without a residual the gauge is a lower bound and must report incomplete")
	}
}

func TestReconcileWithAnUnknownAttributedCostIsIncomplete(t *testing.T) {
	unpriced := pricedItem("b", 0)
	unpriced.Load = LoadedNowCost(UnknownTokens("this harness reports no per-item cost"))
	r := anchoredReport(1000, pricedItem("a", 300), unpriced)
	r.Reconcile()

	if r.Reconciliation.Status != ReconIncomplete {
		t.Fatalf("status = %s, want incomplete", r.Reconciliation.Status)
	}
	if r.Reconciliation.Unaccounted.Known() {
		t.Fatal("subtracting an incomplete attribution from the anchor would produce a meaningless number")
	}
}

func TestReconcileIsIdempotent(t *testing.T) {
	r := anchoredReport(1000, pricedItem("a", 300))
	r.Reconcile()
	first, _ := r.Reconciliation.Unaccounted.Value()
	r.Reconcile()
	second, _ := r.Reconciliation.Unaccounted.Value()
	if first != second {
		t.Fatalf("residual changed across runs: %d then %d; the previous residual is being counted as attribution", first, second)
	}
}

// TestReconcileLabelsTheAttributedSumWithItsWeakestInput guards the figure the
// PROVENANCE column prints beside "attributed".
//
// Almost every item in a real report is estimated from characters, so a sum of
// them is an estimate. Stamping that sum "provider-measured" — which is what it
// used to do unconditionally — puts the strongest label the vocabulary has on
// the softest number in the report, in the one column a reader consults to know
// how much to trust it.
func TestReconcileLabelsTheAttributedSumWithItsWeakestInput(t *testing.T) {
	r := anchoredReport(1000, pricedItem("a", 300), pricedItem("b", 200))
	r.Reconcile()

	if got := r.Reconciliation.Attributed.Prov(); got != TokenEstimated {
		t.Fatalf("attributed provenance = %s, want estimated: every contributing item is an estimate", got)
	}
	if v, ok := r.Reconciliation.Attributed.Value(); !ok || v != 500 {
		t.Fatalf("attributed = %d (known=%v), want 500: degrading the label must not change the number", v, ok)
	}

	// A sum whose inputs really were measured keeps the strong label.
	measured := pricedItem("m", 0)
	measured.Load = LoadedNowCost(Measured(400, "harness usage record"))
	r = anchoredReport(1000, measured)
	r.Reconcile()
	if got := r.Reconciliation.Attributed.Prov(); got != TokenProviderMeasured {
		t.Fatalf("attributed provenance = %s, want provider-measured when every input is measured", got)
	}
}

func TestReconcileDoesNotFabricateACalibration(t *testing.T) {
	r := anchoredReport(1000, pricedItem("a", 300))
	r.Reconcile()
	if r.Calibration != nil {
		t.Fatal("the residual closes against the anchor by construction, so calibrating on it would always report 0% error; Reconcile must leave Calibration nil")
	}
	if r.Reconciliation.Coverage <= 0 {
		t.Fatal("coverage is the real number Reconcile can publish and must be set")
	}
	if got := r.Reconciliation.Coverage; got < 29.9 || got > 30.1 {
		t.Fatalf("coverage = %f%%, want 30%% (300 attributed of a 1000 anchor)", got)
	}
}

func TestFixedTotalExcludesPotential(t *testing.T) {
	deferred := Item{
		ID:      "skill:dataviz",
		Label:   "dataviz",
		Content: CapturedContent(KindListing, "dataviz — charts and graphs"),
		Load:    OnDemandCost(Estimated(100, "chars/4"), Estimated(9000, "chars/4")),
		Origin:  OriginUserConfig,
		Lever:   DirLever("/home/u/.claude/skills/dataviz", "installed skill"),
	}
	r := anchoredReport(1000, pricedItem("a", 300), deferred)
	r.Reconcile()

	attributed, complete := r.AttributedTotal()
	if !complete {
		t.Fatal("every item is priced, so the attributed total must be complete")
	}
	if attributed != 400 {
		t.Fatalf("attributed = %d, want 400 (300 + the 100-token listing line, not the 9000-token body)", attributed)
	}

	total, ok := r.FixedTotal()
	if !ok {
		t.Fatal("an anchored, fully priced report must produce a complete gauge total")
	}
	if total != 1000 {
		t.Fatalf("FixedTotal = %d, want 1000: it must equal the anchor and must never absorb a potential cost", total)
	}

	pot, any := r.PotentialTotal()
	if !any || pot != 9000 {
		t.Fatalf("PotentialTotal = %d (any=%v), want 9000 reported separately", pot, any)
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("report must validate: %v", err)
	}
}

func TestAvailableItemsCostACertainZero(t *testing.T) {
	l := AvailableCost(Estimated(5000, "chars/4"))
	v, ok := l.Actual.Value()
	if !ok || v != 0 {
		t.Fatalf("available actual = %d (known=%v), want a known 0: nothing was sent", v, ok)
	}
	if l.Actual.Prov() != TokenProviderMeasured {
		t.Fatalf("provenance = %s: a not-loaded item costs a certain zero, not an estimated one", l.Actual.Prov())
	}
	if l.Potential == nil {
		t.Fatal("a known potential cost must be retained so the user sees what loading it would cost")
	}
}

func TestOnDemandDropsARedundantPotential(t *testing.T) {
	l := OnDemandCost(Estimated(100, "chars/4"), Estimated(100, "chars/4"))
	if l.Potential != nil {
		t.Fatal("a potential equal to the actual cost is noise and must be dropped")
	}
	if l = OnDemandCost(Estimated(100, "chars/4"), UnknownTokens("body not measured")); l.Potential != nil {
		t.Fatal("an unknown potential must be nil rather than a value-less pointer")
	}
}

func TestCategoryTotalIsIncompleteWhenAnItemIsUnknown(t *testing.T) {
	unpriced := pricedItem("b", 0)
	unpriced.Load = LoadedNowCost(UnknownTokens("no per-item accounting"))
	c := Category{Name: "mcp", Title: "MCP", Items: []Item{pricedItem("a", 250), unpriced}}

	total, complete := c.Total()
	if complete {
		t.Fatal("a category holding an unknown can never present a clean total")
	}
	if total != 250 {
		t.Fatalf("known part = %d, want 250: the caller renders it as \"≥ 250\"", total)
	}
}

func TestRollupItemsAreNotDoubleCounted(t *testing.T) {
	rollup := Item{
		ID:      "memory",
		Label:   "memory (2 files)",
		Content: AbsentContent("group header"),
		Load:    LoadedNowCost(Estimated(999, "chars/4")),
		Origin:  OriginProject,
		Lever:   ImmovableLever("group header"),
		Rollup:  true,
		Children: []Item{
			pricedItem("memory/a", 100),
			pricedItem("memory/b", 200),
		},
	}
	c := Category{Name: "memory-files", Title: "memory", Items: []Item{rollup}}
	total, complete := c.Total()
	if !complete {
		t.Fatal("both children are priced, so the total is complete")
	}
	if total != 300 {
		t.Fatalf("total = %d, want 300: a rollup header and its children must not both count", total)
	}
}

func TestBadgeCollapsesTheWeakerAxis(t *testing.T) {
	tests := []struct {
		name  string
		badge Badge
		want  Confidence
	}{
		{"verbatim and measured", Badge{TextCaptured, TokenProviderMeasured}, ConfidenceHigh},
		{"verbatim but estimated", Badge{TextCaptured, TokenEstimated}, ConfidenceMedium},
		{"reconstructed and measured", Badge{TextReconstructed, TokenProviderMeasured}, ConfidenceMedium},
		{"measured but no text", Badge{TextAbsent, TokenProviderMeasured}, ConfidenceLow},
		{"verbatim but no number", Badge{TextCaptured, TokenUnknown}, ConfidenceLow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.badge.Confidence(); got != tt.want {
				t.Fatalf("confidence = %s, want %s", got, tt.want)
			}
		})
	}
	if got := (Badge{TextCaptured, TokenEstimated}).Short(); got != "CAPT/~est" {
		t.Fatalf("short badge = %q, want CAPT/~est", got)
	}
}

func TestItemBadgeReadsBothAxesFromTheItem(t *testing.T) {
	it := Item{
		ID:      "x",
		Label:   "x",
		Content: CapturedContent(KindListing, "listing"),
		Load:    LoadedNowCost(Estimated(10, "chars/4")),
	}
	got := it.Badge()
	if got.Text != TextCaptured || got.Token != TokenEstimated {
		t.Fatalf("badge = %+v, want captured text with an estimated number", got)
	}
}

func TestSortedPutsActionableAndExpensiveFirst(t *testing.T) {
	immovable := pricedItem("builtin", 9999)
	immovable.Origin = OriginHarnessBuiltin
	immovable.Lever = ImmovableLever("shipped in the binary")

	available := pricedItem("unused", 0)
	available.Load = AvailableCost(Estimated(5000, "chars/4"))

	c := Category{Name: "x", Title: "x", Items: []Item{
		immovable,
		available,
		pricedItem("cheap", 10),
		pricedItem("expensive", 900),
	}}

	got := c.Sorted()
	want := []string{"expensive", "cheap", "unused", "builtin"}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("position %d = %q, want %q (order: actionable, then loaded, then cost desc)", i, got[i].ID, id)
		}
	}
}

func TestValidateRejectsAnEstimateWithNoNote(t *testing.T) {
	bad := pricedItem("a", 100)
	// Constructed directly: the constructor would refuse this, and Validate is
	// the second gate that catches an adapter assembling a struct by hand.
	bad.Load = Load{State: LoadedNow, Actual: TokenCount{known: true, value: 100, prov: TokenEstimated}}

	r := anchoredReport(1000, bad)
	err := r.Validate()
	if err == nil {
		t.Fatal("an estimate with no estimator note must be rejected")
	}
	if !strings.Contains(err.Error(), "estimator note") {
		t.Fatalf("error %q must name the missing note", err)
	}
	if len(r.Violations) == 0 {
		t.Fatal("violations must be recorded on the report so the UI can say it contradicts itself")
	}
}

func TestValidateRejectsAnUnknownWithNoReason(t *testing.T) {
	bad := pricedItem("a", 0)
	bad.Load = Load{State: LoadedNow, Actual: TokenCount{prov: TokenUnknown}}
	if err := anchoredReport(1000, bad).Validate(); err == nil || !strings.Contains(err.Error(), "no reason") {
		t.Fatalf("an unexplained unknown must be rejected, got %v", err)
	}
}

func TestValidateRejectsARedundantPotential(t *testing.T) {
	bad := pricedItem("a", 100)
	same := Estimated(100, "chars/4")
	bad.Load.Potential = &same
	if err := anchoredReport(1000, bad).Validate(); err == nil || !strings.Contains(err.Error(), "equal to its actual cost") {
		t.Fatalf("a potential equal to the actual cost must be rejected, got %v", err)
	}
}

func TestValidateRejectsAvailableItemsThatClaimACost(t *testing.T) {
	bad := pricedItem("a", 100)
	bad.Load.State = Available
	if err := anchoredReport(1000, bad).Validate(); err == nil || !strings.Contains(err.Error(), "positive cost") {
		t.Fatalf("an available item that claims a cost must be rejected, got %v", err)
	}
}

func TestValidateRejectsAbsentTextThatCarriesText(t *testing.T) {
	bad := pricedItem("a", 100)
	bad.Content = Content{Prov: TextAbsent, Text: "but here it is"}
	if err := anchoredReport(1000, bad).Validate(); err == nil || !strings.Contains(err.Error(), "text-absent") {
		t.Fatalf("contradictory content provenance must be rejected, got %v", err)
	}
}

func TestValidateRejectsAnActionableLeverOnHarnessInternals(t *testing.T) {
	bad := pricedItem("a", 100)
	bad.Origin = OriginHarnessBuiltin
	if err := anchoredReport(1000, bad).Validate(); err == nil || !strings.Contains(err.Error(), "harness-builtin") {
		t.Fatalf("offering the user a lever on something they cannot change must be rejected, got %v", err)
	}
}

func TestValidateRejectsALeverWithNoTarget(t *testing.T) {
	noPath := pricedItem("a", 100)
	noPath.Lever = Lever{Kind: LeverEditFile}
	if err := anchoredReport(1000, noPath).Validate(); err == nil || !strings.Contains(err.Error(), "no path") {
		t.Fatalf("a file lever with no path must be rejected, got %v", err)
	}

	noCmd := pricedItem("b", 100)
	noCmd.Lever = Lever{Kind: LeverRunCommand}
	if err := anchoredReport(1000, noCmd).Validate(); err == nil || !strings.Contains(err.Error(), "no command") {
		t.Fatalf("a command lever with no command must be rejected, got %v", err)
	}
}

func TestValidateRejectsAnEmptyRollup(t *testing.T) {
	bad := pricedItem("a", 100)
	bad.Rollup = true
	if err := anchoredReport(1000, bad).Validate(); err == nil || !strings.Contains(err.Error(), "rollup with no children") {
		t.Fatalf("an empty rollup would erase its own cost from every total, got %v", err)
	}
}

func TestValidateRejectsANonMeasuredAnchor(t *testing.T) {
	r := anchoredReport(1000, pricedItem("a", 100))
	r.Anchor = &Anchor{Tokens: Estimated(1000, "chars/4"), Source: "guess"}
	if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "provider-measured") {
		t.Fatalf("only a provider-measured figure may be an anchor, got %v", err)
	}
}

func TestValidateRejectsAnAnchorOnAProjectedReport(t *testing.T) {
	r := anchoredReport(1000, pricedItem("a", 100))
	r.Basis = BasisProjected
	if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "projected report carries an anchor") {
		t.Fatalf("a projection has no session to measure, got %v", err)
	}
}

func TestValidateRejectsDuplicateCategories(t *testing.T) {
	r := anchoredReport(1000, pricedItem("a", 100))
	r.Categories = append(r.Categories, r.Categories[0])
	if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "declared more than once") {
		t.Fatalf("duplicate categories would double-count, got %v", err)
	}
}

func TestValidateAcceptsAWellFormedReport(t *testing.T) {
	r := anchoredReport(1000, pricedItem("a", 300), pricedItem("b", 200))
	r.Reconcile()
	if err := r.Validate(); err != nil {
		t.Fatalf("a well-formed report must validate: %v", err)
	}
	if len(r.Violations) != 0 {
		t.Fatalf("violations = %v, want none", r.Violations)
	}
}

func TestWindowPercentRequiresAKnownWindow(t *testing.T) {
	if _, ok := (WindowInfo{}).Percent(1000); ok {
		t.Fatal("an unknown window must not yield a percentage: a wrong denominator makes an honest numerator misleading")
	}
	pct, ok := WindowInfo{Tokens: 200000, Source: WindowModelDefault}.Percent(20000)
	if !ok || pct != 10 {
		t.Fatalf("percent = %f (ok=%v), want 10", pct, ok)
	}
}

func TestReportCategoryLookup(t *testing.T) {
	r := anchoredReport(1000, pricedItem("a", 100))
	if _, ok := r.Category("memory-files"); !ok {
		t.Fatal("declared category must be findable by name")
	}
	if _, ok := r.Category("skills"); ok {
		t.Fatal("a category the adapter did not declare must not be invented")
	}
}

func TestEstimatorMethodNamesTheHeuristicActuallyUsed(t *testing.T) {
	r := anchoredReport(1000, pricedItem("a", 100))
	if got := r.EstimatorMethod(); !strings.Contains(got, "chars/4") {
		t.Fatalf("EstimatorMethod = %q, want the heuristic the items were priced with", got)
	}
	empty := &Report{Harness: "x"}
	if got := empty.EstimatorMethod(); got != "" {
		t.Fatalf("a report with no estimates must report no method, got %q", got)
	}
}
