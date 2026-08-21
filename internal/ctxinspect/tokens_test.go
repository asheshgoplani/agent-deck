package ctxinspect

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestTokenCountZeroValueIsUnknown(t *testing.T) {
	var tc TokenCount
	if _, ok := tc.Value(); ok {
		t.Fatal("the zero TokenCount must not carry a value: a struct that forgot to set a count must render \"—\", not 0")
	}
	if tc.Prov() != TokenUnknown {
		t.Fatalf("zero value provenance = %s, want unknown", tc.Prov())
	}
	if tc.String() != "—" {
		t.Fatalf("zero value renders %q, want an em dash", tc.String())
	}
}

func TestTokenCountConstructorsRefuseUnprovenancedNumbers(t *testing.T) {
	tests := []struct {
		name string
		got  TokenCount
	}{
		{"measured without a method", Measured(100, "")},
		{"measured without a method, whitespace only", Measured(100, "   ")},
		{"computed without a method", Computed(100, "")},
		{"estimated without a note", Estimated(100, "")},
		{"negative measured", Measured(-1, "usage.input")},
		{"negative estimated", Estimated(-5, "chars/4")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if v, ok := tt.got.Value(); ok {
				t.Fatalf("value %d escaped as known; it must degrade to a described unknown", v)
			}
			if tt.got.Reason() == "" {
				t.Fatal("the degraded unknown must carry a reason")
			}
		})
	}
}

func TestResidualTokensMayBeNegative(t *testing.T) {
	tc := ResidualTokens(-42, "anchor − Σ(attributed)")
	v, ok := tc.Value()
	if !ok {
		t.Fatal("a residual must remain known so the negative can be surfaced")
	}
	if v != -42 {
		t.Fatalf("residual value = %d, want -42 (never clamped)", v)
	}
	if tc.Prov() != TokenResidual {
		t.Fatalf("provenance = %s, want residual", tc.Prov())
	}
}

func TestSumTokensDegradesToUnknownNeverZero(t *testing.T) {
	sum := SumTokens("category total",
		Measured(10, "usage"),
		UnknownTokens("this harness reports no per-item cost"),
		Measured(20, "usage"),
	)
	if v, ok := sum.Value(); ok {
		t.Fatalf("sum = %d with a known flag; an unknown input must make the whole sum unknown", v)
	}
	if !strings.Contains(sum.Reason(), "no per-item cost") {
		t.Fatalf("sum reason %q must carry the underlying reason forward", sum.Reason())
	}
}

func TestSumTokensCarriesWeakestProvenance(t *testing.T) {
	tests := []struct {
		name   string
		inputs []TokenCount
		want   TokenProv
	}{
		{"all measured", []TokenCount{Measured(1, "a"), Measured(2, "b")}, TokenProviderMeasured},
		{"measured plus computed", []TokenCount{Measured(1, "a"), Computed(2, "b")}, TokenComputed},
		{"measured plus estimated", []TokenCount{Measured(1, "a"), Estimated(2, "chars/4")}, TokenEstimated},
		{"estimated plus residual", []TokenCount{Estimated(1, "chars/4"), ResidualTokens(2, "sub")}, TokenResidual},
		{"empty sums to a measured zero", nil, TokenProviderMeasured},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SumTokens("total", tt.inputs...)
			if got.Prov() != tt.want {
				t.Fatalf("provenance = %s, want %s", got.Prov(), tt.want)
			}
		})
	}
}

func TestSubTokensNeverClamps(t *testing.T) {
	got := SubTokens(Measured(100, "anchor"), Measured(140, "attributed"), "anchor − Σ(attributed)")
	v, ok := got.Value()
	if !ok {
		t.Fatal("a negative difference must stay known so the bug is visible")
	}
	if v != -40 {
		t.Fatalf("difference = %d, want -40", v)
	}
}

func TestSubTokensPropagatesUnknown(t *testing.T) {
	if got := SubTokens(UnknownTokens("no anchor"), Measured(1, "x"), "m"); got.Known() {
		t.Fatal("an unknown minuend must yield an unknown difference")
	}
	if got := SubTokens(Measured(1, "x"), UnknownTokens("no attribution"), "m"); got.Known() {
		t.Fatal("an unknown subtrahend must yield an unknown difference")
	}
}

func TestTokenCountJSONEmitsNullForUnknown(t *testing.T) {
	b, err := json.Marshal(UnknownTokens("nothing to measure"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"tokens":null`) {
		t.Fatalf("unknown encoded as %s; it must emit null so no consumer can read a 0 that does not exist", b)
	}
}

func TestTokenCountJSONRoundTrip(t *testing.T) {
	tests := []TokenCount{
		Measured(1234, "usage.input+cache_creation+cache_read"),
		Computed(99, "tiktoken"),
		Estimated(4321, "chars/4"),
		ResidualTokens(-7, "anchor − Σ(attributed)"),
		UnknownTokens("unsupported harness"),
	}
	for _, want := range tests {
		t.Run(want.Prov().String(), func(t *testing.T) {
			b, err := json.Marshal(want)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got TokenCount
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			wv, wok := want.Value()
			gv, gok := got.Value()
			if wok != gok || wv != gv || got.Prov() != want.Prov() {
				t.Fatalf("round trip lost fidelity: got (%d,%v,%s) want (%d,%v,%s)", gv, gok, got.Prov(), wv, wok, want.Prov())
			}
		})
	}
}

func TestTokenCountJSONRejectsSmuggledValue(t *testing.T) {
	// A hand-edited fixture that names a number without naming a method must
	// not decode into a usable count.
	var got TokenCount
	if err := json.Unmarshal([]byte(`{"tokens":500,"provenance":"estimated"}`), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Known() {
		t.Fatal("a number decoded without a method must degrade to unknown")
	}
}

func TestEstimatorUsesPerKindDivisors(t *testing.T) {
	e := DefaultEstimator()
	text := strings.Repeat("x", 400)

	markdown, ok := e.Estimate(KindMarkdown, text).Value()
	if !ok {
		t.Fatal("markdown estimate must be known")
	}
	listing, ok := e.Estimate(KindListing, text).Value()
	if !ok {
		t.Fatal("listing estimate must be known")
	}
	// 400/2.34 and 400/2.98. The point of the assertion is not the exact
	// integers but that the two kinds are still priced apart: collapsing them
	// to one divisor is what made the estimator 30% low on memory files while
	// looking acceptable on catalogue text.
	if markdown != 171 {
		t.Fatalf("400 chars of dense markdown = %d tokens, want 171 (chars / %.2f)", markdown, divMarkdown)
	}
	if listing != 134 {
		t.Fatalf("400 chars of catalogue text = %d tokens, want 134 (chars / %.2f)", listing, divListing)
	}
	if markdown <= listing {
		t.Fatal("dense markdown tokenizes denser than catalogue text; pricing it cheaper inverts the measured corpus")
	}
}

// TestEstimatorMatchesTheOracleCorpus is the test the recalibration exists for.
//
// The figures on the right are what Claude Code's own /context reported for
// these exact files and catalogue in a live session on 2026-07-29 — the model's
// tokenizer speaking. The old table (3.8 chars/token for both kinds) produced
// 16,656 for the memory corpus against a true 26,793: 38% low, and every
// "you could save N tokens" built on it was wrong by the same margin.
//
// The tolerances are the bands the estimator itself declares. A change that
// widens the error past its own advertised band is a change that has made the
// report dishonest, whatever else it improved.
func TestEstimatorMatchesTheOracleCorpus(t *testing.T) {
	e := DefaultEstimator()

	cases := []struct {
		name      string
		kind      ContentKind
		chars     int
		oracle    int
		tolerance float64 // fraction
	}{
		// Per-file memory corpus: character counts as the walk would price
		// them, against the per-file figures /context printed.
		{"a 43 KB ancestor CLAUDE.md", KindMarkdown, 42426, 18400, 0.08},
		{"a 12 KB project CLAUDE.md", KindMarkdown, 11607, 4600, 0.08},
		{"a 7.6 KB user-global CLAUDE.md", KindMarkdown, 7565, 3400, 0.08},
		{"a 1 KB project auto-memory MEMORY.md", KindMarkdown, 993, 393, 0.08},
		// The whole memory category, which is the number the panel headlines.
		{"memory category total", KindMarkdown, 62591, 26793, 0.02},
		// The skill catalogue, split per skill and summed.
		{"skill catalogue total", KindListing, 16856, 5650, 0.02},
	}

	for _, tc := range cases {
		got, ok := e.EstimateChars(tc.kind, tc.chars).Value()
		if !ok {
			t.Fatalf("%s: estimate must be known", tc.name)
		}
		err := math.Abs(float64(got-tc.oracle)) / float64(tc.oracle)
		if err > tc.tolerance {
			t.Errorf("%s: estimated %d against a measured %d (%.1f%% off), outside the declared ±%.0f%%",
				tc.name, got, tc.oracle, err*100, tc.tolerance*100)
		}
	}
}

// TestEstimatorCarriesItsErrorBar pins the promise that a figure never reaches
// a screen claiming more precision than it has.
func TestEstimatorCarriesItsErrorBar(t *testing.T) {
	e := DefaultEstimator()

	measured := e.EstimateChars(KindMarkdown, 42426)
	if measured.RelErr() <= 0 {
		t.Fatal("a divisor derived from a corpus must publish the residual spread it was derived with")
	}
	low, high, ok := measured.Range()
	if !ok {
		t.Fatal("a count with a band must expose the interval")
	}
	v, _ := measured.Value()
	if low >= v || high <= v {
		t.Fatalf("range [%d, %d] does not straddle the point estimate %d", low, high, v)
	}
	if !strings.Contains(measured.Method(), "±") {
		t.Fatalf("method %q must state the band: every text surface prints the method, and none of them may print a bare point estimate", measured.Method())
	}
	if !strings.Contains(measured.Method(), "measured") {
		t.Fatalf("method %q must name the evidence behind the divisor", measured.Method())
	}

	// A kind with no corpus must say so and must be banded wider than one with.
	unmeasured := e.EstimateChars(KindJSON, 42426)
	if unmeasured.RelErr() <= measured.RelErr() {
		t.Fatalf("an unmeasured kind (±%.0f%%) must not claim a tighter band than a measured one (±%.0f%%)",
			unmeasured.RelErr()*100, measured.RelErr()*100)
	}
	if !strings.Contains(unmeasured.Method(), "not measured") {
		t.Fatalf("method %q must admit that this kind has no evidence behind it", unmeasured.Method())
	}
}

// TestSumTokensAddsErrorBandsLinearly pins the combination rule. The inputs
// share one estimator calibrated on one corpus, so their errors move together;
// combining them in quadrature would shrink a category's band toward zero and
// manufacture confidence out of item count.
func TestSumTokensAddsErrorBandsLinearly(t *testing.T) {
	a := EstimatedWithin(1000, 0.10, "a")
	b := EstimatedWithin(1000, 0.10, "b")

	sum := SumTokens("category", a, b)
	if got, want := sum.RelErr(), 0.10; math.Abs(got-want) > 1e-9 {
		t.Fatalf("sum band = %.4f, want %.4f: correlated errors add linearly, so the band of equal-band items is unchanged", got, want)
	}

	// One unbounded contributor makes the whole sum unbounded. A band that
	// covers only the items that happen to declare one is not a band.
	mixed := SumTokens("category", a, Estimated(1000, "unbounded"))
	if mixed.RelErr() != 0 {
		t.Fatalf("sum band = %.4f, want 0: one unbounded input must not be papered over", mixed.RelErr())
	}
	if v, ok := mixed.Value(); !ok || v != 2000 {
		t.Fatalf("sum = %d/%v, want 2000: dropping the band must not drop the number", v, ok)
	}
}

// TestTokenCountBandSurvivesJSON keeps the wire form and the screens in
// agreement: a consumer reading the JSON must not see a bare point estimate
// where the terminal shows a band.
func TestTokenCountBandSurvivesJSON(t *testing.T) {
	orig := EstimatedWithin(1000, 0.08, "chars/token: 2340 chars / 2.34 (markdown, ±8%)")
	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, field := range []string{`"rel_err"`, `"tokens_low"`, `"tokens_high"`} {
		if !strings.Contains(string(raw), field) {
			t.Fatalf("encoded form %s is missing %s", raw, field)
		}
	}
	var back TokenCount
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.RelErr() != orig.RelErr() {
		t.Fatalf("round-tripped band = %v, want %v", back.RelErr(), orig.RelErr())
	}

	// An unbounded estimate must not gain a zero-width band on the way through.
	raw, err = json.Marshal(Estimated(1000, "unbounded"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "tokens_low") {
		t.Fatalf("encoded form %s invents an interval for an unbounded estimate", raw)
	}
}

func TestEstimatorAlwaysNamesItsMethod(t *testing.T) {
	e := DefaultEstimator()
	got := e.Estimate(KindMarkdown, "hello world")
	if got.Prov() != TokenEstimated {
		t.Fatalf("provenance = %s, want estimated", got.Prov())
	}
	if !strings.Contains(got.Method(), "chars") {
		t.Fatalf("method %q must name the heuristic", got.Method())
	}
	if !strings.Contains(got.Method(), "markdown") {
		t.Fatalf("method %q must name the content kind", got.Method())
	}
}

func TestEstimatorCountsRunesNotBytes(t *testing.T) {
	e := DefaultEstimator()
	ascii, _ := e.Estimate(KindProse, strings.Repeat("a", 40)).Value()
	multibyte, _ := e.Estimate(KindProse, strings.Repeat("é", 40)).Value()
	if ascii != multibyte {
		t.Fatalf("40 ascii chars = %d tokens but 40 multi-byte chars = %d; byte length is overstating multi-byte content", ascii, multibyte)
	}
}

func TestEstimatorGuardsAgainstZeroDivisor(t *testing.T) {
	e := Estimator{Name: "broken"} // no divisors, no default
	got, ok := e.Estimate(KindProse, strings.Repeat("x", 40)).Value()
	if !ok {
		t.Fatal("a misconfigured estimator must still produce a bounded number, not panic or divide by zero")
	}
	if got != 15 {
		t.Fatalf("fallback divisor produced %d tokens, want 15 (chars / %.2f)", got, divUnmeasured)
	}
}

func TestEstimateCharsRejectsNegative(t *testing.T) {
	if DefaultEstimator().EstimateChars(KindProse, -1).Known() {
		t.Fatal("a negative character count must yield an unknown, not a negative estimate")
	}
}

func TestCalibrateRequiresAnAnchor(t *testing.T) {
	if got := Calibrate(0, 500, "chars/4"); got != nil {
		t.Fatal("an error bound against a zero anchor is worse than no bound; Calibrate must return nil")
	}
	if got := Calibrate(-1, 500, "chars/4"); got != nil {
		t.Fatal("a negative anchor is not an anchor")
	}
}

func TestCalibrateSignedError(t *testing.T) {
	c := Calibrate(1000, 943, "chars/4")
	if c == nil {
		t.Fatal("calibration must exist for a positive anchor")
	}
	if math.Abs(c.ErrorPct-(-5.7)) > 0.001 {
		t.Fatalf("ErrorPct = %f, want -5.7 (signed: under-count is negative)", c.ErrorPct)
	}
	if !strings.Contains(c.Summary(), "5.7%") {
		t.Fatalf("summary %q must publish the bound", c.Summary())
	}
}

func TestCalibrationToleranceGate(t *testing.T) {
	c := Calibrate(1000, 1200, "chars/4") // +20%
	if err := c.CheckTolerance(25); err != nil {
		t.Fatalf("20%% error inside a 25%% band must pass, got %v", err)
	}
	err := c.CheckTolerance(10)
	if err == nil {
		t.Fatal("20% error outside a 10% band must fail so CI catches estimator drift")
	}
	if !errors.Is(err, ErrEstimatorTolerance) {
		t.Fatalf("error %v must wrap ErrEstimatorTolerance so CI can assert on it", err)
	}
	var nilCal *Calibration
	if err := nilCal.CheckTolerance(1); err != nil {
		t.Fatalf("a nil calibration has nothing to check and must pass, got %v", err)
	}
}

func TestTokenProvOrderingPutsResidualBelowEstimate(t *testing.T) {
	// A residual is defined as anchor − Σ(attributed), so its error is at
	// least the combined error of the estimates it subtracted. It can never be
	// presented as more trustworthy than its worst input.
	if tokenProvRank(TokenResidual) <= tokenProvRank(TokenEstimated) {
		t.Fatal("residual must rank weaker than estimated")
	}
	if tokenProvRank(TokenProviderMeasured) != 0 {
		t.Fatal("provider-measured must be the strongest provenance")
	}
	if tokenProvRank(TokenUnknown) != 4 {
		t.Fatal("unknown must be the weakest provenance")
	}
}
