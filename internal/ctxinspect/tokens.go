package ctxinspect

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// TokenCount is a token count that cannot exist without a provenance and a
// method describing how it was produced.
//
// Every field is unexported on purpose: there is no literal form, no exported
// setter, and no way to mutate one after construction. Every number that
// reaches a renderer therefore came through [Measured], [Computed],
// [Estimated], [ResidualTokens] or arithmetic over those. The zero value is a
// valid unknown, so a struct that forgot to set a count reports "—" rather than
// a confident zero.
type TokenCount struct {
	known  bool
	value  int
	prov   TokenProv
	method string  // how the number was produced; required whenever known
	reason string  // why there is no number; only meaningful when !known
	relErr float64 // fractional error band; 0 means "not bounded", never "exact"
}

// Measured builds a count the model provider or the harness reported. method
// names the exact fields read, e.g. "usage.input+cache_creation+cache_read".
func Measured(v int, method string) TokenCount {
	return newTokenCount(v, TokenProviderMeasured, method)
}

// Computed builds a count obtained by tokenizing real bytes with the model's
// own tokenizer.
func Computed(v int, method string) TokenCount {
	return newTokenCount(v, TokenComputed, method)
}

// Estimated builds a heuristic count whose error is not bounded. note names the
// heuristic and is mandatory — [Report.Validate] rejects an estimate without
// one.
//
// Prefer [EstimatedWithin]: an estimate that carries no band is indistinguishable
// on screen from one that has been checked against something.
func Estimated(v int, note string) TokenCount {
	return newTokenCount(v, TokenEstimated, note)
}

// EstimatedWithin builds a heuristic count that declares how far it has been
// observed to land from the truth. relErr is a fraction (0.08 is ±8%) measured
// against real counterparts, not a guess about the guess; pass 0 only when there
// is genuinely nothing to bound it with.
//
// The band exists because a point estimate printed to the token is a claim of
// precision the heuristic does not have. Carrying it lets a renderer print
// "~18k ±8%" and lets a reader tell a figure checked against 62k characters of
// evidence from one that was never checked at all.
func EstimatedWithin(v int, relErr float64, note string) TokenCount {
	t := newTokenCount(v, TokenEstimated, note)
	if t.known && relErr > 0 {
		t.relErr = relErr
	}
	return t
}

// ResidualTokens builds a count derived by subtracting attributed costs from a
// measured anchor. The value may be negative; see [Report.Reconcile].
func ResidualTokens(v int, method string) TokenCount {
	return TokenCount{known: true, value: v, prov: TokenResidual, method: strings.TrimSpace(method)}
}

// UnknownTokens builds an explicit "no number exists" with the reason a
// renderer shows on hover or at L2.
func UnknownTokens(reason string) TokenCount {
	return TokenCount{prov: TokenUnknown, reason: strings.TrimSpace(reason)}
}

// newTokenCount refuses negative values for the provenances where a negative is
// meaningless (only a residual may legitimately go negative) and refuses a
// number whose method was not named. Both degrade to a described unknown rather
// than escaping as a bare number.
func newTokenCount(v int, prov TokenProv, method string) TokenCount {
	method = strings.TrimSpace(method)
	if method == "" {
		return UnknownTokens(fmt.Sprintf("a %s count was produced without naming its method and was discarded", prov))
	}
	if v < 0 {
		return UnknownTokens(fmt.Sprintf("a %s count was negative (%d) via %q and was discarded", prov, v, method))
	}
	return TokenCount{known: true, value: v, prov: prov, method: method}
}

// Value returns the number and whether one exists. Renderers must go through
// it; ok=false means print "—", never 0.
func (t TokenCount) Value() (int, bool) { return t.value, t.known }

// Known reports whether a number exists.
func (t TokenCount) Known() bool { return t.known }

// Prov returns the provenance. An unknown count reports [TokenUnknown]
// regardless of how it was built.
func (t TokenCount) Prov() TokenProv {
	if !t.known {
		return TokenUnknown
	}
	return t.prov
}

// Method returns how the number was produced. Empty for an unknown.
func (t TokenCount) Method() string {
	if !t.known {
		return ""
	}
	return t.method
}

// Reason returns why no number exists. Empty for a known count.
func (t TokenCount) Reason() string {
	if t.known {
		return ""
	}
	return t.reason
}

// RelErr returns the fractional error band the count declares, or 0 when it
// declares none. A measured or computed count has no band because it is not an
// estimate; an estimate with a 0 band is one nothing was available to check.
func (t TokenCount) RelErr() float64 {
	if !t.known {
		return 0
	}
	return t.relErr
}

// Range returns the interval the count claims to lie in, and whether one exists.
// A count with no band has no range: reporting [v, v] would turn "unbounded" into
// "exact", which is the error this type exists to prevent.
func (t TokenCount) Range() (low, high int, ok bool) {
	if !t.known || t.relErr <= 0 {
		return 0, 0, false
	}
	delta := math.Abs(float64(t.value)) * t.relErr
	return int(math.Round(float64(t.value) - delta)), int(math.Round(float64(t.value) + delta)), true
}

// BandString renders the band as "±8%", or "" when there is none.
func (t TokenCount) BandString() string {
	if !t.known || t.relErr <= 0 {
		return ""
	}
	return fmt.Sprintf("±%s%%", trimFloat(t.relErr*100))
}

// String renders the count for logs and plain-text output.
func (t TokenCount) String() string {
	if !t.known {
		return "—"
	}
	if t.prov == TokenEstimated {
		if band := t.BandString(); band != "" {
			return fmt.Sprintf("~%d %s", t.value, band)
		}
		return fmt.Sprintf("~%d", t.value)
	}
	return fmt.Sprintf("%d", t.value)
}

// trimFloat renders a float with at most one decimal and no trailing ".0", so a
// band reads "±8%" rather than "±8.0%".
func trimFloat(v float64) string {
	s := strconv.FormatFloat(v, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0")
}

// tokenCountJSON is the wire form. tokens is a pointer so an unknown encodes as
// null, never as 0 — a JSON consumer must not be able to read a number that
// does not exist.
type tokenCountJSON struct {
	Tokens *int      `json:"tokens"`
	Prov   TokenProv `json:"provenance"`
	Method string    `json:"method,omitempty"`
	Reason string    `json:"reason,omitempty"`
	// RelErr is the fractional band, omitted when the count declares none so a
	// consumer cannot read an absent band as a zero-width one.
	RelErr float64 `json:"rel_err,omitempty"`
	// Low and High are the band expressed in tokens, so a consumer that only
	// wants the interval does not have to redo the arithmetic — and cannot get
	// it wrong in a way that disagrees with what the screens show.
	Low  *int `json:"tokens_low,omitempty"`
	High *int `json:"tokens_high,omitempty"`
}

// MarshalJSON encodes the count, emitting null for an unknown.
func (t TokenCount) MarshalJSON() ([]byte, error) {
	out := tokenCountJSON{Prov: t.Prov(), Method: t.Method(), Reason: t.Reason(), RelErr: t.RelErr()}
	if t.known {
		v := t.value
		out.Tokens = &v
	}
	if low, high, ok := t.Range(); ok {
		out.Low, out.High = &low, &high
	}
	return json.Marshal(out)
}

// UnmarshalJSON decodes a count, applying the same rules as the constructors so
// a hand-edited fixture cannot smuggle in a number without provenance.
func (t *TokenCount) UnmarshalJSON(b []byte) error {
	var in tokenCountJSON
	if err := json.Unmarshal(b, &in); err != nil {
		return fmt.Errorf("ctxinspect: decoding token count: %w", err)
	}
	if in.Tokens == nil || in.Prov == TokenUnknown {
		*t = UnknownTokens(in.Reason)
		return nil
	}
	if in.Prov == TokenResidual {
		*t = ResidualTokens(*in.Tokens, in.Method)
		return nil
	}
	if in.Prov == TokenEstimated {
		*t = EstimatedWithin(*in.Tokens, in.RelErr, in.Method)
		return nil
	}
	*t = newTokenCount(*in.Tokens, in.Prov, in.Method)
	return nil
}

// SumTokens adds counts, degrading rather than lying.
//
// Any unknown input makes the whole sum unknown — never zero, and never a
// partial total presented as a whole one. The result carries the weakest
// provenance of its inputs. Summing nothing yields a measured zero, because
// "no items" really does cost nothing.
func SumTokens(method string, counts ...TokenCount) TokenCount {
	if len(counts) == 0 {
		return Measured(0, method)
	}
	total := 0
	// absErr accumulates the band in tokens rather than in percent. The inputs
	// share one estimator calibrated on one corpus, so their errors are
	// correlated, not independent noise: adding them in quadrature would shrink
	// the band of a hundred items to nearly nothing and claim a precision the
	// heuristic cannot have. Linear addition is the honest combination for a
	// systematic bias.
	absErr := 0.0
	unbounded := false
	prov := TokenProviderMeasured
	for _, c := range counts {
		v, ok := c.Value()
		if !ok {
			return UnknownTokens(fmt.Sprintf("sum via %q is unknown: %s", method, c.Reason()))
		}
		total += v
		if c.Prov() == TokenEstimated {
			if c.relErr <= 0 {
				unbounded = true
			}
			absErr += math.Abs(float64(v)) * c.relErr
		}
		prov = weakestTokenProv(prov, c.Prov())
	}
	if prov == TokenResidual {
		return ResidualTokens(total, method)
	}
	if prov != TokenEstimated || unbounded || total == 0 || absErr <= 0 {
		// One unbounded contributor makes the whole sum unbounded: a band that
		// covers only the parts that happen to declare one is not a band.
		return newTokenCount(total, prov, method)
	}
	return EstimatedWithin(total, absErr/math.Abs(float64(total)), method)
}

// SubTokens returns a − b and never clamps.
//
// A negative result is the shipped self-check for a parser bug: it means we
// attributed more than the provider measured. Hiding it behind a max(0, …) is
// exactly how the prior-art tools come to report a plausible-looking lie, so
// the negative is preserved and [Report.Reconcile] escalates it.
func SubTokens(a, b TokenCount, method string) TokenCount {
	av, aok := a.Value()
	bv, bok := b.Value()
	if !aok {
		return UnknownTokens(fmt.Sprintf("difference via %q is unknown: %s", method, a.Reason()))
	}
	if !bok {
		return UnknownTokens(fmt.Sprintf("difference via %q is unknown: %s", method, b.Reason()))
	}
	return ResidualTokens(av-bv, method)
}

// ContentKind classifies bytes so the estimator can pick a divisor. Prompt text
// and a JSON tool schema tokenize very differently; one global divisor would be
// wrong for both.
type ContentKind uint8

const (
	// KindProse is natural-language text.
	KindProse ContentKind = iota
	// KindMarkdown is instruction/memory files: prose with headings, lists and
	// fenced code.
	KindMarkdown
	// KindJSON is serialized structure: tool schemas, MCP definitions.
	KindJSON
	// KindCode is source code.
	KindCode
	// KindListing is a generated catalogue of name/description lines.
	KindListing
	// KindPath is a bare filesystem path or identifier list.
	KindPath
)

var (
	contentKindNames = map[ContentKind]string{
		KindProse:    "prose",
		KindMarkdown: "markdown",
		KindJSON:     "json",
		KindCode:     "code",
		KindListing:  "listing",
		KindPath:     "path",
	}
	contentKindByName = invertEnum(contentKindNames)
)

// String returns the wire/display name.
func (k ContentKind) String() string { return enumName(k, contentKindNames, "prose") }

// MarshalJSON encodes the value as its wire name.
func (k ContentKind) MarshalJSON() ([]byte, error) { return marshalEnum(k, contentKindNames) }

// UnmarshalJSON decodes the value from its wire name.
func (k *ContentKind) UnmarshalJSON(b []byte) error {
	return unmarshalEnum(b, contentKindByName, k, "content kind")
}

// KindDivisor is the characters-per-token figure for one content kind, together
// with how far a single item priced with it has been seen to land from the
// truth, and what evidence says so.
//
// The three fields travel together on purpose. A divisor with no band invites a
// reader to treat its output as exact, and a band with no basis is a number
// about a number with nothing underneath it.
type KindDivisor struct {
	// CharsPerToken is the divisor.
	CharsPerToken float64
	// RelErr is the fractional band for one item priced with this divisor —
	// 0.08 for ±8%. It is the observed spread of residuals against real
	// counterparts, not a confidence interval in the statistical sense: the
	// sample is small and the errors are systematic.
	RelErr float64
	// Basis names the evidence in one clause, and says plainly when there is
	// none. It reaches the screen, so it is written for a reader.
	Basis string
}

// Estimator turns bytes into an estimated token count.
//
// # Why this is an estimate and not a measurement
//
// The obvious better answer is to run the model's own tokenizer. It was looked
// for and it does not exist for us:
//
//   - Anthropic publishes no tokenizer for the Claude 3+ / Fable families. The
//     old public claude BPE table covers Claude 1/2 and is a different
//     vocabulary; using it would trade a documented approximation for an
//     undocumented one.
//   - The provider's count_tokens endpoint is exact but needs credentials and
//     the network. This panel opens from a keypress on a host that runs dozens
//     of sessions against one rotating OAuth token; billing an API call and
//     aggravating a documented rotation race to render a cost breakdown is not
//     a trade worth making, and an offline host would simply get nothing.
//   - A foreign BPE table (cl100k/o200k via a tiktoken port) would be a
//     megabytes-large dependency that is still measurably wrong for this model
//     family — a heavier way to be approximate.
//
// So the package estimates, and does three things to keep an estimate honest:
// it derives each divisor from a real counterpart rather than from folklore, it
// publishes the band and the evidence for every figure it prints, and it bounds
// the total against the provider-measured anchor where a session has one (see
// [Calibration]).
type Estimator struct {
	// Divisors maps a content kind to its calibrated divisor. A missing kind
	// falls back to Default.
	Divisors map[ContentKind]KindDivisor
	// Default is the divisor for kinds absent from Divisors.
	Default KindDivisor
	// Name identifies the estimator in every method string it produces, so a
	// report says which heuristic generated its numbers.
	Name string
}

// Calibration corpus, captured 2026-07-29 by running Claude Code's own
// /context inside a live session (Fable 5, 1m window) and recording what it
// reported per file and per skill. Those figures are the model's own tokenizer
// speaking, which is the only ground truth available offline.
//
// The two measured kinds and their evidence:
//
//	dense markdown  4 CLAUDE.md/MEMORY.md files, 62,591 chars -> 26,793 tokens
//	                = 2.336 chars/token; per-file residuals at 2.34 span
//	                -5%..+8%.
//	catalogue text  52 skill catalogue entries, 16,856 chars -> 5,650 tokens
//	                = 2.983 chars/token; every entry the capture reported at
//	                100 tokens or more lands within ±4.2% (the wider misses are
//	                all on 20-40 token entries, where the capture's own
//	                rounding to the nearest 10 is ±16%).
//
// The previous table (4.0 prose / 3.8 markdown, the conventional English
// chars-per-token figure) ran 30-40% low on exactly this content, because dense
// instruction markdown is not English prose: absolute paths, emoji, table
// pipes, heading runs and fenced code all tokenize far denser than the folklore
// figure assumes.
const (
	// divMarkdown prices dense instruction markdown.
	divMarkdown = 2.34
	// divListing prices generated catalogue text.
	divListing = 2.98
	// divUnmeasured is the midpoint of the two measured corpora, used for the
	// kinds no counterpart was available for.
	divUnmeasured = 2.66
)

// The basis clauses are short because they are repeated on every item's method
// string, which is printed by every text surface. The full evidence is the
// comment above; what a row needs to carry is whether its divisor was measured
// or assumed.
const (
	basisMarkdown = "divisor measured on 62,591 chars of memory files against Claude Code's own count"
	basisListing  = "divisor measured on 16,856 chars of skill catalogue against Claude Code's own count"
	basisProse    = "divisor borrowed from the measured catalogue corpus; no prose-only corpus exists"
	// basisUnmeasured is deliberately blunt. A kind with no evidence must say
	// so on the screen rather than borrow the credibility of the kinds that
	// have some.
	basisUnmeasured = "divisor not measured for this kind; midpoint of the two measured corpora"
)

// DefaultEstimator returns the shipped estimator, calibrated against the
// /context capture described above.
//
// Divisors are per content kind because the evidence demands it: the same
// heuristic that is right to within 2% on dense markdown is 27% wrong on
// catalogue text. Kinds with no counterpart in the corpus get the midpoint and
// a ±30% band, which is what "we do not know" looks like when it still has to
// produce a number.
func DefaultEstimator() Estimator {
	return Estimator{
		Name:    "chars/token calibrated on a Claude Code /context capture",
		Default: KindDivisor{CharsPerToken: divUnmeasured, RelErr: 0.30, Basis: basisUnmeasured},
		Divisors: map[ContentKind]KindDivisor{
			KindProse:    {CharsPerToken: divListing, RelErr: 0.15, Basis: basisProse},
			KindMarkdown: {CharsPerToken: divMarkdown, RelErr: 0.08, Basis: basisMarkdown},
			KindJSON:     {CharsPerToken: divUnmeasured, RelErr: 0.30, Basis: basisUnmeasured},
			KindCode:     {CharsPerToken: divUnmeasured, RelErr: 0.30, Basis: basisUnmeasured},
			KindListing:  {CharsPerToken: divListing, RelErr: 0.05, Basis: basisListing},
			KindPath:     {CharsPerToken: divUnmeasured, RelErr: 0.30, Basis: basisUnmeasured},
		},
	}
}

// divisor returns the calibrated divisor for a kind, guarding against a zero or
// negative divisor in a hand-built estimator.
func (e Estimator) divisor(kind ContentKind) KindDivisor {
	if d, ok := e.Divisors[kind]; ok && d.CharsPerToken > 0 {
		return d
	}
	if e.Default.CharsPerToken > 0 {
		return e.Default
	}
	return KindDivisor{CharsPerToken: divUnmeasured, RelErr: 0.30, Basis: basisUnmeasured}
}

// EstimateChars estimates the token cost of a known character count. It is used
// when the bytes are too large to keep in memory but their size is known.
func (e Estimator) EstimateChars(kind ContentKind, chars int) TokenCount {
	if chars < 0 {
		return UnknownTokens("negative character count")
	}
	d := e.divisor(kind)
	v := int(math.Round(float64(chars) / d.CharsPerToken))
	// The band goes into the method string as well as into the field: every
	// text surface already prints the method, so an estimate cannot reach a
	// screen wearing a precision it does not have, whatever the renderer does.
	note := fmt.Sprintf("%s: %d chars / %.2f (%s, ±%s%%",
		e.estimatorName(), chars, d.CharsPerToken, kind, trimFloat(d.RelErr*100))
	if basis := strings.TrimSpace(d.Basis); basis != "" {
		note += ", " + basis
	}
	return EstimatedWithin(v, d.RelErr, note+")")
}

// Estimate estimates the token cost of text. Character count is used rather
// than byte length so multi-byte content is not systematically overstated.
func (e Estimator) Estimate(kind ContentKind, text string) TokenCount {
	return e.EstimateChars(kind, len([]rune(text)))
}

// estimatorName returns the estimator's name, defaulting so a method string is
// never anonymous.
func (e Estimator) estimatorName() string {
	if n := strings.TrimSpace(e.Name); n != "" {
		return n
	}
	return "chars-per-token heuristic"
}

// Calibration reports how far an estimated figure lands from an independently
// measured one. It converts an unfalsifiable guess into a bounded,
// self-reported, continuously-checked one at zero network cost, and it is
// rendered verbatim in the overview footer.
//
// The estimated sum must be established *independently* of the anchor. A
// report's own [Report.FixedTotal] is not a valid input: the residual is
// defined as anchor − attributed, so that sum closes against the anchor by
// construction and would always report 0% error. [Report.Reconcile] therefore
// never populates Calibration; it publishes [Reconciliation.Coverage] — the
// share of the measured total that attribution explained — which is a real
// number about a real quantity. Calibration is populated by an adapter or by
// the verification harness when it genuinely holds a measured counterpart for
// content it also estimated (for example a corpus replay that compares an
// estimated prefix against the provider figure for the same prefix).
type Calibration struct {
	// AnchorTokens is the provider-measured fixed-prefix total.
	AnchorTokens int `json:"anchor_tokens"`
	// EstimatedSum is our attributed estimates plus the residual.
	EstimatedSum int `json:"estimated_sum"`
	// ErrorPct is signed: negative means we under-count.
	ErrorPct float64 `json:"error_pct"`
	// Method names the heuristic the estimates came from.
	Method string `json:"method"`
}

// Calibrate compares an estimated sum against a measured anchor. It returns nil
// when there is no usable anchor, because an error bound against nothing is
// worse than no bound at all.
func Calibrate(anchor, estimatedSum int, method string) *Calibration {
	if anchor <= 0 {
		return nil
	}
	return &Calibration{
		AnchorTokens: anchor,
		EstimatedSum: estimatedSum,
		ErrorPct:     (float64(estimatedSum-anchor) / float64(anchor)) * 100,
		Method:       method,
	}
}

// Summary renders the one-line footer the overview prints.
func (c *Calibration) Summary() string {
	if c == nil {
		return "no measured anchor for this session; per-item figures are unbounded estimates"
	}
	sign := "+"
	if c.ErrorPct < 0 {
		sign = "−"
	}
	return fmt.Sprintf("per-item figures are estimates (%s); on this session they sum to within %s%.1f%% of the measured total",
		c.Method, sign, math.Abs(c.ErrorPct))
}

// ErrEstimatorTolerance is returned by [Calibration.CheckTolerance] when the
// estimator has drifted outside its declared band. CI asserts on it so chars/4
// cannot quietly rot as harnesses change.
var ErrEstimatorTolerance = errors.New("ctxinspect: estimator error outside declared tolerance")

// CheckTolerance reports whether the calibration error is within tolerancePct
// (an absolute percentage). A nil calibration passes, because there is nothing
// to check.
func (c *Calibration) CheckTolerance(tolerancePct float64) error {
	if c == nil {
		return nil
	}
	if math.Abs(c.ErrorPct) > tolerancePct {
		return fmt.Errorf("%w: %.1f%% exceeds ±%.1f%% (anchor %d, estimated %d, method %q)",
			ErrEstimatorTolerance, c.ErrorPct, tolerancePct, c.AnchorTokens, c.EstimatedSum, c.Method)
	}
	return nil
}
