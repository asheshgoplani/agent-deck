package ctxinspect

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Basis answers the question "was this measured, or projected?".
//
// It is orthogonal to [LoadState]: Basis is a property of the whole report
// (did we parse a real session's startup records, or compute what a fresh
// session would load), while LoadState is a property of one item (is this in
// the prefix, deferred, or merely on disk).
type Basis uint8

const (
	// BasisObserved: we parsed startup records a real session actually wrote.
	// This is the only basis that can carry an [Anchor].
	BasisObserved Basis = iota
	// BasisProjected: no session records exist, so we computed what would load
	// from configuration and the filesystem. There is no ground-truth anchor,
	// every number is computed or estimated, and the residual is unknowable.
	BasisProjected
)

var (
	basisNames = map[Basis]string{
		BasisObserved:  "observed",
		BasisProjected: "projected",
	}
	basisByName = invertEnum(basisNames)
)

// String returns the wire/display name.
func (b Basis) String() string { return enumName(b, basisNames, "projected") }

// Describe returns the sentence the header prints under the title.
func (b Basis) Describe() string {
	if b == BasisObserved {
		return "observed from this session's own startup records"
	}
	return "projected for a fresh session; no ground-truth anchor"
}

// MarshalJSON encodes the value as its wire name.
func (b Basis) MarshalJSON() ([]byte, error) { return marshalEnum(b, basisNames) }

// UnmarshalJSON decodes the value from its wire name.
func (b *Basis) UnmarshalJSON(data []byte) error {
	return unmarshalEnum(data, basisByName, b, "basis")
}

// Severity grades a [Caveat] for rendering and for CI.
type Severity uint8

const (
	// SeverityInfo: worth knowing, nothing is wrong.
	SeverityInfo Severity = iota
	// SeverityWarn: a number is less trustworthy than it looks.
	SeverityWarn
	// SeverityBug: the report contradicts itself. Something in the adapter or
	// the harness format is broken and the report says so on screen.
	SeverityBug
)

var (
	severityNames = map[Severity]string{
		SeverityInfo: "info",
		SeverityWarn: "warn",
		SeverityBug:  "bug",
	}
	severityByName = invertEnum(severityNames)
)

// String returns the wire/display name.
func (s Severity) String() string { return enumName(s, severityNames, "info") }

// MarshalJSON encodes the value as its wire name.
func (s Severity) MarshalJSON() ([]byte, error) { return marshalEnum(s, severityNames) }

// UnmarshalJSON decodes the value from its wire name.
func (s *Severity) UnmarshalJSON(b []byte) error {
	return unmarshalEnum(b, severityByName, s, "severity")
}

// Caveat is a machine-readable qualification on a report. Caveats are rendered
// as a footer and are part of the JSON output, so a consumer can gate on them
// instead of parsing prose.
type Caveat struct {
	// Code is a stable identifier, e.g. "anchor-unavailable-resumed".
	Code string `json:"code"`
	// Message is the human sentence shown to the user.
	Message string `json:"message"`
	// Severity grades the caveat.
	Severity Severity `json:"severity"`
	// Category optionally scopes the caveat to one category name.
	Category string `json:"category,omitempty"`
}

// WindowSource records how the context-window size was established. The window
// is never hardcoded: a wrong denominator turns an honest numerator into a
// misleading percentage.
type WindowSource uint8

const (
	// WindowUnknown: we could not establish a window size.
	WindowUnknown WindowSource = iota
	// WindowHarnessReported: the harness wrote the window into its own records.
	WindowHarnessReported
	// WindowEnvOverride: an environment variable set the window.
	WindowEnvOverride
	// WindowSettings: harness or agent-deck configuration set the window.
	WindowSettings
	// WindowModelDefault: derived from the model identifier.
	WindowModelDefault
	// WindowModelFamily: assumed from the model's family because the exact
	// release was never taught to this build.
	//
	// It is a separate source, not a flavour of [WindowModelDefault], because
	// it is the one window figure in this package that nothing established: it
	// is an inference from sibling releases. Every surface that prints a
	// percentage derived from it must say so, which it can only do if the
	// distinction survives into the report and the JSON.
	WindowModelFamily
)

var (
	windowSourceNames = map[WindowSource]string{
		WindowUnknown:         "unknown",
		WindowHarnessReported: "harness-reported",
		WindowEnvOverride:     "env",
		WindowSettings:        "settings",
		WindowModelDefault:    "model-default",
		WindowModelFamily:     "model-family",
	}
	windowSourceByName = invertEnum(windowSourceNames)
)

// String returns the wire/display name.
func (s WindowSource) String() string { return enumName(s, windowSourceNames, "unknown") }

// MarshalJSON encodes the value as its wire name.
func (s WindowSource) MarshalJSON() ([]byte, error) { return marshalEnum(s, windowSourceNames) }

// UnmarshalJSON decodes the value from its wire name.
func (s *WindowSource) UnmarshalJSON(b []byte) error {
	return unmarshalEnum(b, windowSourceByName, s, "window source")
}

// WindowInfo is the context-window size and where that figure came from.
type WindowInfo struct {
	// Tokens is the window size. Zero means unknown.
	Tokens int `json:"tokens"`
	// Source records how Tokens was established.
	Source WindowSource `json:"source"`
	// Detail names the exact origin, e.g. the settings key or record field.
	Detail string `json:"detail,omitempty"`
}

// Known reports whether a usable window size exists.
func (w WindowInfo) Known() bool { return w.Tokens > 0 && w.Source != WindowUnknown }

// Assumed reports whether the size was inferred rather than established.
//
// A percentage computed from an assumed window is still worth showing — the
// alternative is a screen with no gauge at all — but it must never be printed
// with the same confidence as one computed from a window the harness reported.
func (w WindowInfo) Assumed() bool { return w.Known() && w.Source == WindowModelFamily }

// windowInfoJSON is the wire form. Tokens is a pointer so an unknown window
// emits null rather than 0.
type windowInfoJSON struct {
	Tokens *int         `json:"tokens"`
	Source WindowSource `json:"source"`
	Detail string       `json:"detail,omitempty"`
}

// MarshalJSON emits an unknown window's size as null.
//
// "tokens": 0 next to "source": "unknown" is honest only to a consumer that
// reads both fields. One that computes used/window — the obvious thing to do
// with a window — divides by zero and gets an answer that is either +Inf or a
// crash, from a document whose whole promise is that an unknown is never
// dressed up as a number. null cannot be misread that way.
func (w WindowInfo) MarshalJSON() ([]byte, error) {
	out := windowInfoJSON{Source: w.Source, Detail: w.Detail}
	if w.Known() {
		tokens := w.Tokens
		out.Tokens = &tokens
	}
	return json.Marshal(out)
}

// UnmarshalJSON accepts both the null and the numeric form, so a document this
// package wrote round-trips through it unchanged.
func (w *WindowInfo) UnmarshalJSON(b []byte) error {
	var in windowInfoJSON
	if err := json.Unmarshal(b, &in); err != nil {
		return err
	}
	w.Source = in.Source
	w.Detail = in.Detail
	w.Tokens = 0
	if in.Tokens != nil {
		w.Tokens = *in.Tokens
	}
	return nil
}

// Percent returns used/window as a percentage and whether it is meaningful.
func (w WindowInfo) Percent(used int) (float64, bool) {
	if !w.Known() {
		return 0, false
	}
	return float64(used) / float64(w.Tokens) * 100, true
}

// Anchor is a provider-measured total for the fixed prefix — the one number in
// a report that nothing of ours produced.
//
// It exists only when the harness recorded a cold-start turn. A resumed or
// compacted session's first turn measures a warm prefix, which is a different
// quantity; adapters must set Anchor to nil in that case and add a caveat
// rather than present a number that means something else.
type Anchor struct {
	// Tokens is the measured fixed-prefix size. Its provenance is always
	// [TokenProviderMeasured]; [Report.Validate] enforces that.
	Tokens TokenCount `json:"tokens"`
	// Source names the record the figure was read from.
	Source string `json:"source"`
	// At is when the measured turn happened, when the record carries it.
	At time.Time `json:"at,omitzero"`
	// UpperBound marks a measured figure that covers more than the fixed
	// prefix, so the true prefix is at most this large.
	//
	// It exists because "provider-measured" and "measures exactly the fixed
	// prefix" are different claims, and the second one is the one every reader
	// assumes. When the harness records its startup catalogues after an earlier
	// turn, the only request that carried them also carried that turn's output,
	// its tool results and whatever the user attached — on a real corpus that
	// is worth up to 28,948 tokens more than the startup catalogues explain.
	// The figure is still measured and still the best available; what it is not
	// is exact, and a renderer, a --json consumer and verify.Compare all have to
	// be able to say so rather than infer it from a caveat's prose.
	UpperBound bool `json:"upper_bound,omitempty"`
	// UpperBoundReason states, in one sentence, what else the measurement
	// covers. Empty when UpperBound is false.
	UpperBoundReason string `json:"upper_bound_reason,omitempty"`
}

// IsUpperBound reports whether the anchor is known to overstate the fixed
// prefix. A nil anchor is not an upper bound; it is no measurement at all.
func (a *Anchor) IsUpperBound() bool { return a != nil && a.UpperBound }

// Content is the bytes behind an item, with an explicit statement of how good
// those bytes are.
type Content struct {
	// Prov says whether these bytes are verbatim, reconstructed, or absent.
	Prov TextProv `json:"provenance"`
	// Kind classifies the bytes for the estimator.
	Kind ContentKind `json:"kind"`
	// Text is the content. Empty when Prov is [TextAbsent].
	Text string `json:"text,omitempty"`
	// Chars is the character count of the *full* content, which exceeds
	// len(Text) when Truncated is set.
	Chars int `json:"chars"`
	// Truncated reports that Text holds only a bounded prefix of the content.
	// Reads are bounded so a pathological file cannot exhaust memory; a
	// truncated body is still honest as long as it says so.
	Truncated bool `json:"truncated,omitempty"`
	// Note explains an absence or a truncation.
	Note string `json:"note,omitempty"`
}

// AbsentContent builds content we know is in the window and cannot see.
func AbsentContent(note string) Content {
	return Content{Prov: TextAbsent, Note: note}
}

// CapturedContent builds content read back verbatim from a record the harness
// itself wrote.
func CapturedContent(kind ContentKind, text string) Content {
	return Content{Prov: TextCaptured, Kind: kind, Text: text, Chars: len([]rune(text))}
}

// ReconstructedContent builds content we rebuilt from the same sources the
// harness reads. note must say what was rebuilt and from where.
func ReconstructedContent(kind ContentKind, text, note string) Content {
	return Content{Prov: TextReconstructed, Kind: kind, Text: text, Chars: len([]rune(text)), Note: note}
}

// Item is one contributor to the context window.
type Item struct {
	// ID is stable across runs so a UI can preserve selection.
	ID string `json:"id"`
	// Label is the one-line name shown in a list row.
	Label string `json:"label"`
	// Detail is the secondary line, e.g. a count or a short path.
	Detail string `json:"detail,omitempty"`
	// Content is the bytes behind the item.
	Content Content `json:"content"`
	// Load is what the item costs now and what it could cost.
	Load Load `json:"load"`
	// Origin records who put it there.
	Origin Origin `json:"origin"`
	// Lever is what the user can do about it.
	Lever Lever `json:"lever"`
	// Children segment the item for L3 display (per-file, per-skill, …).
	Children []Item `json:"children,omitempty"`
	// Rollup marks an item whose own cost is the sum of Children. A rollup is
	// skipped by every total so a group header and its members cannot both be
	// counted; without the flag the invariant would be a convention rather
	// than something [Report.Validate] can enforce.
	Rollup bool `json:"rollup,omitempty"`
	// Informational marks an item whose cost is already counted inside another
	// item, so it must not be added to any total.
	//
	// It is the row for a part of a block that could not be split: the block's
	// whole cost is accounted for once, and the part's share of it is unknown.
	// Without this flag such a row has to carry an unknown count, which turns
	// an otherwise complete total into an incomplete one — 52% of a real deck
	// reported "≥" for a sum that was in fact whole. The alternative, a zero,
	// is the false certainty this whole model exists to prevent. So the row is
	// shown (it is real, and it usually carries the lever the user wants) and
	// excluded from the arithmetic.
	Informational bool `json:"informational,omitempty"`
	// Caveats qualify this item specifically.
	Caveats []Caveat `json:"caveats,omitempty"`
}

// Badge collapses the item's two provenance axes for rendering. It is the only
// place that collapse happens.
func (i Item) Badge() Badge {
	return Badge{Text: i.Content.Prov, Token: i.Load.Actual.Prov()}
}

// Actionable reports whether the user has a lever on this item.
func (i Item) Actionable() bool { return i.Lever.Kind.Actionable() }

// countable returns the items that contribute to a total: leaves, plus
// non-rollup parents (whose own Actual is the whole cost, with children being
// presentational segmentation only).
func (i Item) countable() []TokenCount {
	if i.Informational {
		return nil
	}
	if i.Rollup {
		var out []TokenCount
		for _, c := range i.Children {
			out = append(out, c.countable()...)
		}
		return out
	}
	return []TokenCount{i.Load.Actual}
}

// PotentialTokens returns the item's potential cost when it is both known and
// larger than the actual cost. Potential is reported separately and is never
// added to any total.
func (i Item) PotentialTokens() (int, bool) {
	if i.Load.Potential == nil {
		return 0, false
	}
	return i.Load.Potential.Value()
}

// Category is an adapter-declared grouping of items.
//
// Category names come from the adapter, in the adapter's order. The UI never
// invents one, and an adapter that cannot report a category honestly omits it
// rather than emitting a zeroed row — Codex genuinely has no "skills" category
// in the Claude sense, and fabricating an empty one would imply it costs
// nothing rather than that it does not exist.
type Category struct {
	// Name is the stable identifier, e.g. "memory-files".
	Name string `json:"name"`
	// Title is the display heading.
	Title string `json:"title"`
	// Description is one sentence explaining what lands here.
	Description string `json:"description,omitempty"`
	// Items are the contributors, in adapter order. Use [Category.Sorted] for
	// the default display order.
	Items []Item `json:"items"`
	// Notes are category-scoped explanations rendered under the heading.
	Notes []string `json:"notes,omitempty"`
	// Unobserved marks a category the adapter could not establish the contents
	// of, as opposed to one it established to be empty.
	//
	// The two look identical in the data — no items — and print identically as
	// "0" unless something tells them apart. "0 MCP instructions" reads as "you
	// have no MCP servers", which is a claim about the user's configuration
	// that an unread transcript cannot support. A category flagged here renders
	// its total as unknown; it still contributes nothing to the attributed sum,
	// because nothing was attributed — whatever is actually there is inside the
	// unattributed remainder, which is exactly where an unnamed cost belongs.
	Unobserved bool `json:"unobserved,omitempty"`
}

// Established reports whether this category's emptiness is a finding rather
// than a gap. It is the single predicate behind every "we never looked here"
// rendering, so the total, the badge and the item count cannot disagree about
// which of the two a blank row is.
func (c Category) Established() bool { return !c.Unobserved || len(c.Items) > 0 }

// DisplayTotal is the figure to render for this category: [Category.Total] for
// an observed one, and an unknown for a category whose contents were never
// established. Only the display differs — the arithmetic is unchanged, because
// what was attributed here really is nothing.
func (c Category) DisplayTotal() (tokens int, complete bool) {
	if !c.Established() {
		return 0, false
	}
	return c.Total()
}

// DisplayBadge is the provenance to render for this category's summary row, and
// whether there is one to render at all.
//
// [Category.Badge] answers TextAbsent for a category with no items, and
// "absent" means "we established that it is not there" — the one thing an
// unread transcript cannot establish. An unobserved category has no badge: its
// provenance cell renders as the same unknown its token cell does.
func (c Category) DisplayBadge() (Badge, bool) {
	if !c.Established() {
		return Badge{}, false
	}
	return c.Badge(), true
}

// DisplayItemCount is the "(N items)" text for a summary row. A count of zero
// is a measurement; a category nobody read has no count to report.
func (c Category) DisplayItemCount() (n int, known bool) {
	if !c.Established() {
		return 0, false
	}
	return len(c.Items), true
}

// Total sums the category's actual costs.
//
// complete is false when any contributing count is unknown. A category holding
// an unknown can never present a clean total; the caller renders "≥ N" in that
// case, and the returned sum is the known part only.
func (c Category) Total() (tokens int, complete bool) {
	complete = true
	for _, it := range c.Items {
		for _, tc := range it.countable() {
			v, ok := tc.Value()
			if !ok {
				complete = false
				continue
			}
			tokens += v
		}
	}
	return tokens, complete
}

// PotentialTotal sums the potential costs of the category's items. It is
// reported in its own dimmed column and never contributes to [Category.Total].
func (c Category) PotentialTotal() (tokens int, any bool) {
	for _, it := range c.Items {
		if v, ok := it.PotentialTokens(); ok {
			tokens += v
			any = true
		}
	}
	return tokens, any
}

// Badge collapses the category's items onto one badge for a summary row.
//
// The weakest axis of any contributing item wins, on both axes independently:
// a category is only as trustworthy as its worst member, and a single absent
// or unpriced item must not be hidden behind a majority of good ones. Rollup
// parents are skipped so a group header cannot outvote the members whose cost
// it merely restates.
//
// It lives here rather than in a renderer because [Badge] is the package's
// single provenance-collapse point; a caller that re-derived this would be free
// to disagree with [Item.Badge] about what the same data means.
func (c Category) Badge() Badge {
	badge := Badge{Text: TextCaptured, Token: TokenProviderMeasured}
	seen := false
	var walk func(items []Item)
	walk = func(items []Item) {
		for _, it := range items {
			if it.Informational {
				// Its cost is another item's; letting it vote would grade the
				// category on a figure that is not part of the category's total.
				continue
			}
			if it.Rollup {
				walk(it.Children)
				continue
			}
			seen = true
			badge.Text = weakestTextProv(badge.Text, it.Content.Prov)
			badge.Token = weakestTokenProv(badge.Token, it.Load.Actual.Prov())
		}
	}
	walk(c.Items)
	if !seen {
		// An empty category asserts nothing. Reporting it as fully captured and
		// provider-measured would be a claim about content that does not exist.
		return Badge{Text: TextAbsent, Token: TokenUnknown}
	}
	return badge
}

// Sorted returns the items in default display order:
//
//  1. actionable before immovable — leading with harness internals the user
//     cannot touch wastes the top of the screen;
//  2. loaded before on-demand before available — an item that costs nothing
//     today must not head a list about what is costing you, even though its
//     certain zero would otherwise sort as a known value;
//  3. actual cost descending, with known costs above unknown ones;
//  4. among items with no known cost, potential cost descending, so the one
//     row that could buy the most is not left wherever the alphabet put it;
//  5. label, so the order is stable and comparable against a fixture.
//
// Together that answers the question the panel exists for: what is costing me
// now, and what could I remove.
func (c Category) Sorted() []Item {
	out := make([]Item, len(c.Items))
	copy(out, c.Items)
	sort.SliceStable(out, func(a, b int) bool { return itemRanksBefore(out[a], out[b]) })
	return out
}

// itemRanksBefore is the single ordering rule behind [Category.Sorted] and both
// flat rankings. It lives here so the per-category list and the breakdown can
// never disagree about what "ranked" means.
func itemRanksBefore(ia, ib Item) bool {
	if ia.Actionable() != ib.Actionable() {
		return ia.Actionable()
	}
	if ia.Load.State != ib.Load.State {
		return ia.Load.State < ib.Load.State
	}
	va, oka := ia.Load.Actual.Value()
	vb, okb := ib.Load.Actual.Value()
	if oka != okb {
		return oka // known costs rank above unknowns
	}
	if oka && va != vb {
		return va > vb
	}
	if !oka {
		// Neither costs a known amount today. Rank by what removing it could
		// save, so the biggest lever is not buried by an alphabetical tie.
		pa, hasA := ia.PotentialTokens()
		pb, hasB := ib.PotentialTokens()
		if hasA != hasB {
			return hasA
		}
		if hasA && pa != pb {
			return pa > pb
		}
	}
	if ia.Label != ib.Label {
		return ia.Label < ib.Label
	}
	return ia.ID < ib.ID
}

// HistoryLine is the single orientation line the overview prints about the
// conversation. Conversation analysis is deliberately out of scope: the panel
// is about the fixed startup overhead the user can actually clean up.
type HistoryLine struct {
	// Tokens is the conversation's occupancy, when known.
	Tokens TokenCount `json:"tokens"`
	// Turns is the number of turns, when known. Zero means unreported.
	Turns int `json:"turns,omitempty"`
	// Note explains what the figure covers.
	Note string `json:"note,omitempty"`
}

// ReconStatus is the verdict of [Report.Reconcile].
type ReconStatus uint8

const (
	// ReconNoAnchor: there is no provider-measured total to reconcile against,
	// so the residual is unknowable. This is the honest state for a projected
	// report or a resumed session.
	ReconNoAnchor ReconStatus = iota
	// ReconOK: attributed costs plus the residual equal the anchor, and the
	// residual is non-negative.
	ReconOK
	// ReconFailed: attributed costs exceed the measured anchor. Something is
	// double-counted or mis-parsed. The report refuses to present a clean total.
	ReconFailed
	// ReconIncomplete: at least one attributed count is unknown, so the
	// residual cannot be computed even though an anchor exists.
	ReconIncomplete
)

var (
	reconStatusNames = map[ReconStatus]string{
		ReconNoAnchor:   "no-anchor",
		ReconOK:         "ok",
		ReconFailed:     "failed",
		ReconIncomplete: "incomplete",
	}
	reconStatusByName = invertEnum(reconStatusNames)
)

// String returns the wire/display name.
func (s ReconStatus) String() string { return enumName(s, reconStatusNames, "no-anchor") }

// MarshalJSON encodes the value as its wire name.
func (s ReconStatus) MarshalJSON() ([]byte, error) { return marshalEnum(s, reconStatusNames) }

// UnmarshalJSON decodes the value from its wire name.
func (s *ReconStatus) UnmarshalJSON(b []byte) error {
	return unmarshalEnum(b, reconStatusByName, s, "reconciliation status")
}

// Reconciliation is the report's arithmetic self-check, computed by
// [Report.Reconcile] and shown on the Verify tab.
type Reconciliation struct {
	Status ReconStatus `json:"status"`
	// Anchor is the provider-measured fixed-prefix total, when one exists.
	Anchor TokenCount `json:"anchor"`
	// Attributed is the sum of every category's actual costs.
	Attributed TokenCount `json:"attributed"`
	// Unaccounted is anchor − attributed. It is never clamped.
	Unaccounted TokenCount `json:"unaccounted"`
	// Coverage is the share of the measured anchor that attribution explained,
	// as a percentage. It is meaningful only when Status is [ReconOK]; it is
	// the report's honest "how much of this did we actually account for"
	// figure, and it is not an accuracy claim about any individual estimate.
	Coverage float64 `json:"coverage_pct,omitempty"`
	// Message is the sentence rendered next to the verdict.
	Message string `json:"message"`
}

// OK reports whether the report reconciled cleanly.
func (r Reconciliation) OK() bool { return r.Status == ReconOK }

// Report is one session's complete context inventory.
type Report struct {
	// Harness is the tool name the report describes, e.g. "claude".
	Harness string `json:"harness"`
	// Adapter is the adapter that produced the report.
	Adapter string `json:"adapter"`
	// SessionID identifies the inspected session, when one exists.
	SessionID string `json:"session_id,omitempty"`
	// ProjectPath is the session's working directory.
	ProjectPath string `json:"project_path,omitempty"`
	// Model is the model identifier, when known.
	Model string `json:"model,omitempty"`
	// Window is the context-window size and its source.
	Window WindowInfo `json:"window"`
	// Basis says whether this was observed or projected.
	Basis Basis `json:"basis"`
	// Anchor is the provider-measured fixed-prefix total, or nil.
	Anchor *Anchor `json:"anchor,omitempty"`
	// Categories are adapter-declared, in adapter order.
	Categories []Category `json:"categories"`
	// Unaccounted is the explicit residual, produced by [Report.Reconcile]. It
	// is a first-class item with its own row and is never distributed into
	// siblings.
	Unaccounted *Item `json:"unaccounted,omitempty"`
	// History is the single conversation orientation line.
	History *HistoryLine `json:"history,omitempty"`
	// Calibration bounds the estimator's error against an independently
	// measured figure. It is nil unless an adapter or the verification harness
	// genuinely holds such a figure; see [Calibration] for why
	// [Report.Reconcile] never fills it in.
	Calibration *Calibration `json:"calibration,omitempty"`
	// Reconciliation is the arithmetic self-check.
	Reconciliation Reconciliation `json:"reconciliation"`
	// Caveats qualify the whole report.
	Caveats []Caveat `json:"caveats,omitempty"`
	// Capabilities is what the adapter declared it could do, captured before
	// inspection ran so it survives an inspection failure.
	Capabilities Capabilities `json:"capabilities"`
	// Violations lists invariant breaches found by [Report.Validate]. A
	// non-empty list means the report contradicts itself and the UI must say
	// so rather than render it as trustworthy.
	Violations []string `json:"violations,omitempty"`
	// GeneratedAt is when the report was produced.
	GeneratedAt time.Time `json:"generated_at"`
}

// Category returns the named category and whether it exists.
func (r *Report) Category(name string) (Category, bool) {
	for _, c := range r.Categories {
		if c.Name == name {
			return c, true
		}
	}
	return Category{}, false
}

// AttributedTotal sums every category's actual costs.
//
// complete is false when any contributing count is unknown, in which case the
// returned sum covers only the known part and the caller must render it as a
// lower bound.
func (r *Report) AttributedTotal() (tokens int, complete bool) {
	complete = true
	for _, c := range r.Categories {
		v, ok := c.Total()
		tokens += v
		if !ok {
			complete = false
		}
	}
	return tokens, complete
}

// attributedCounts returns every count that contributes to the attributed sum,
// in category then item order, with rollup parents expanded to their children
// exactly as [Category.Total] does.
//
// It exists so the attributed sum can be built with [SumTokens] rather than
// re-labelled by hand: the sum is only as trustworthy as its weakest input, and
// the provenance is printed in a column the user reads.
func (r *Report) attributedCounts() []TokenCount {
	var out []TokenCount
	for _, c := range r.Categories {
		for _, it := range c.Items {
			out = append(out, it.countable()...)
		}
	}
	return out
}

// FixedTotal is the gauge number: the size of the fixed startup prefix.
//
// It sums only actual costs — attributed items plus the explicit residual —
// and never includes any [Load.Potential]. When the report reconciled against
// an anchor, it equals the anchor exactly, by construction.
func (r *Report) FixedTotal() (tokens int, complete bool) {
	tokens, complete = r.AttributedTotal()
	if r.Unaccounted == nil {
		// No residual was computed: whatever the harness does not expose is
		// unmeasured, so the total is a lower bound rather than a total.
		return tokens, false
	}
	v, ok := r.Unaccounted.Load.Actual.Value()
	if !ok {
		return tokens, false
	}
	return tokens + v, complete
}

// ActionableTotal counts the items the user has a lever on, and sums what those
// items cost today.
//
// It is the answer to the question this whole feature exists for — "what can I
// change, and what does it buy me" — and until it was on the first screen the
// overview had no verb on it at all: a gauge, six data rows, a legend, and
// nothing to do. The only actionable content was one level down, under a
// heading nobody had a reason to open.
//
// complete is false when any contributing count is unknown, in which case
// tokens covers only the known part and the caller must render it as a lower
// bound. Deferred items contribute their *actual* cost, which is usually zero:
// removing something that is not loaded saves nothing today, and counting its
// potential here would put a saving on screen that no measurement supports.
// Rollup parents are expanded to their children so a group and its members
// cannot both be counted; informational rows never count, exactly as they never
// contribute to a total.
func (r *Report) ActionableTotal() (n int, tokens int, complete bool) {
	complete = true
	var walk func(items []Item)
	walk = func(items []Item) {
		for _, it := range items {
			if it.Informational {
				continue
			}
			if it.Rollup {
				walk(it.Children)
				continue
			}
			if !it.Actionable() {
				continue
			}
			n++
			v, ok := it.Load.Actual.Value()
			if !ok {
				complete = false
				continue
			}
			tokens += v
		}
	}
	for _, c := range r.Categories {
		walk(c.Items)
	}
	return n, tokens, complete
}

// PotentialTotal sums every item's potential cost. It is reported beside the
// gauge and is never added to it.
func (r *Report) PotentialTotal() (tokens int, any bool) {
	for _, c := range r.Categories {
		v, ok := c.PotentialTotal()
		if ok {
			tokens += v
			any = true
		}
	}
	return tokens, any
}

// AddCaveat appends a report-level caveat.
func (r *Report) AddCaveat(code, message string, sev Severity) {
	r.Caveats = append(r.Caveats, Caveat{Code: code, Message: message, Severity: sev})
}

// hasCaveat reports whether a caveat with the given code is already recorded,
// so finalising a report twice cannot duplicate it.
func (r *Report) hasCaveat(code string) bool {
	for _, c := range r.Caveats {
		if c.Code == code {
			return true
		}
	}
	return false
}

// unaccountedItemID is the stable identifier of the residual row.
const unaccountedItemID = "unaccounted"

// Reconcile computes the residual against the anchor and records the verdict.
//
// It is the report's own self-check and it never clamps. A negative residual
// means we attributed more than the provider measured — a double-count or a
// mis-parse — and it is surfaced as [ReconFailed] with the negative value
// intact. Clamping it to zero is exactly how a plausible-looking lie gets
// shipped.
//
// noteDiskDerivedFreshness says the thing every disk-derived row leaves unsaid:
// it describes the file as it is NOW, and a session that is already running is
// still sending the copy it booted with.
//
// Reading the filesystem at the moment the report is built is the right source —
// it is the same one the harness reads — but it answers a slightly different
// question from the one the screen appears to answer. Trim a CLAUDE.md and the
// report immediately shows the smaller figure for a session that will keep
// sending the old text on every turn until it restarts, so the reader is shown a
// saving they have not made yet. Nothing on disk can say what a live process is
// holding in memory, so the honest move is to name which question is being
// answered rather than to let one number stand for both.
//
// Info rather than Warn: no figure here is wrong, and the qualifier applies
// identically to every reconstructed row rather than singling one out. The
// column legend carries the short form on the first screen, where the RECON
// badge itself appears.
func (r *Report) noteDiskDerivedFreshness() {
	n := 0
	for _, c := range r.Categories {
		for _, it := range c.Items {
			if it.Content.Prov == TextReconstructed {
				n++
			}
		}
	}
	if n == 0 {
		return
	}
	r.AddCaveat("disk-derived-as-of-now", fmt.Sprintf(
		"%d row(s) here are marked RECON, meaning they were read from disk as it is right now. A session that is already running keeps the copy it loaded at startup, so editing one of these files changes the figure here before it changes what the running session actually sends: restart the session for the two to agree.",
		n), SeverityInfo)
}

// Reconcile is idempotent: it replaces any previously computed residual, so
// finalising a report twice cannot double-count.
func (r *Report) Reconcile() {
	r.dropUnaccounted()

	attributed, complete := r.AttributedTotal()
	// Built with SumTokens rather than stamped Measured: almost every item in
	// the sum is estimated, and a sum of estimates that calls itself
	// provider-measured is the exact class of plausible-looking lie this
	// package exists to prevent. SumTokens degrades the label to the weakest
	// contributing input, which is what the PROVENANCE column then prints.
	attributedCount := SumTokens("sum of attributed item costs", r.attributedCounts()...)
	if !complete {
		attributedCount = UnknownTokens("at least one attributed item has no token count")
	}

	if r.Anchor == nil {
		r.Reconciliation = Reconciliation{
			Status:      ReconNoAnchor,
			Anchor:      UnknownTokens("no provider-measured fixed-prefix total for this session"),
			Attributed:  attributedCount,
			Unaccounted: UnknownTokens("no anchor to subtract from; the unattributed remainder is unknowable"),
			Message:     "no provider-measured anchor: nothing to reconcile against, so the residual is unknown rather than zero",
		}
		r.Unaccounted = r.buildUnaccountedItem(r.Reconciliation.Unaccounted,
			"The harness did not record a cold-start prompt total for this session, so the size of anything we could not attribute cannot be derived.")
		return
	}

	anchorTokens := r.Anchor.Tokens
	if !complete {
		r.Reconciliation = Reconciliation{
			Status:      ReconIncomplete,
			Anchor:      anchorTokens,
			Attributed:  attributedCount,
			Unaccounted: UnknownTokens("an attributed item has no token count, so the residual cannot be derived"),
			Message:     "at least one attributed item has no token count: the residual cannot be derived and is reported as unknown",
		}
		r.Unaccounted = r.buildUnaccountedItem(r.Reconciliation.Unaccounted,
			"One or more categories could not be priced, so subtracting them from the measured total would produce a meaningless number.")
		return
	}

	residual := SubTokens(anchorTokens, attributedCount, "anchor − Σ(attributed)")
	status := ReconOK
	message := "attributed costs plus the unattributed remainder equal the measured total"
	if v, ok := residual.Value(); ok && v < 0 {
		status = ReconFailed
		message = fmt.Sprintf("RECONCILIATION FAILED: attributed costs exceed the measured total by %d tokens — something is double-counted or mis-parsed", -v)
	}

	r.Reconciliation = Reconciliation{
		Status:      status,
		Anchor:      anchorTokens,
		Attributed:  attributedCount,
		Unaccounted: residual,
		Message:     message,
	}
	if av, ok := anchorTokens.Value(); ok && av > 0 && status == ReconOK {
		r.Reconciliation.Coverage = float64(attributed) / float64(av) * 100
	}
	explain := "Content the harness sends but never writes to disk — the base system prompt and the built-in tool schemas. Measured by subtraction from the provider-reported total, not estimated."
	if status == ReconFailed {
		explain = "Negative: we attributed more than the provider measured. This is a bug in the report, not a property of your context."
	}
	r.Unaccounted = r.buildUnaccountedItem(residual, explain)
}

// EstimatorMethod reports the heuristic behind the report's estimates, read
// from the first estimated count it can find so a footer names the method
// actually used rather than a hardcoded string. It returns an empty string when
// the report contains no estimates.
func (r *Report) EstimatorMethod() string {
	for _, c := range r.Categories {
		for _, it := range c.Items {
			if it.Load.Actual.Prov() == TokenEstimated {
				if m := it.Load.Actual.Method(); m != "" {
					if idx := strings.Index(m, ":"); idx > 0 {
						return m[:idx]
					}
					return m
				}
			}
		}
	}
	return ""
}

// EstimatorBandPct reports the widest per-item error band any estimate in the
// report declares, as a percentage, and whether any estimate declares one.
//
// The widest rather than the average: a footer that quotes a report's best-case
// band would understate exactly the rows a reader is most likely to act on. A
// report whose estimates declare no band at all returns ok=false, which the
// footers render as "unbounded" rather than as ±0%.
func (r *Report) EstimatorBandPct() (pct float64, ok bool) {
	for _, c := range r.Categories {
		for _, it := range c.Items {
			if it.Load.Actual.Prov() != TokenEstimated {
				continue
			}
			if e := it.Load.Actual.RelErr(); e > 0 {
				ok = true
				if e*100 > pct {
					pct = e * 100
				}
			}
		}
	}
	return pct, ok
}

// dropUnaccounted clears a previously computed residual so Reconcile can be
// re-run without accumulating rows.
func (r *Report) dropUnaccounted() { r.Unaccounted = nil }

// buildUnaccountedItem wraps the residual count in a first-class item so every
// renderer treats it as a row rather than a footnote.
func (r *Report) buildUnaccountedItem(count TokenCount, note string) *Item {
	return &Item{
		ID:      unaccountedItemID,
		Label:   "system prompt + built-in tool schemas",
		Detail:  "unattributed remainder",
		Content: AbsentContent(note),
		Load:    Load{State: LoadedNow, Actual: count},
		Origin:  OriginHarnessBuiltin,
		Lever:   ImmovableLever("shipped inside the agent binary; not configurable"),
	}
}

// Validate checks the invariants the report's honesty guarantees rest on and
// records every breach in [Report.Violations].
//
// It performs no IO and is unit-tested directly. A validation failure is a bug
// in the adapter or a change in the harness format, not a user-facing error:
// the report is still returned, and the UI marks it as contradicting itself
// rather than silently rendering it as trustworthy.
func (r *Report) Validate() error {
	var v []string

	if strings.TrimSpace(r.Harness) == "" {
		v = append(v, "report has no harness name")
	}

	if r.Anchor != nil {
		if r.Anchor.Tokens.Prov() != TokenProviderMeasured {
			v = append(v, fmt.Sprintf("anchor provenance is %s: only a provider-measured figure may be an anchor", r.Anchor.Tokens.Prov()))
		}
		if r.Basis == BasisProjected {
			v = append(v, "a projected report carries an anchor: a projection has no session to measure")
		}
	}

	seen := make(map[string]bool, len(r.Categories))
	for _, c := range r.Categories {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			v = append(v, "a category has no name")
			continue
		}
		if seen[name] {
			v = append(v, fmt.Sprintf("category %q is declared more than once", name))
		}
		seen[name] = true
		for _, it := range c.Items {
			v = append(v, validateItem(name, it)...)
		}
		if c.Unobserved && len(c.Items) > 0 {
			v = append(v, fmt.Sprintf("category %q is marked unobserved but carries %d item(s): a category whose contents could not be established cannot also list them", name, len(c.Items)))
		}
		// A category's completeness must follow from its items, not be asserted.
		if _, complete := c.Total(); !complete {
			if !categoryHasUnknown(c) {
				v = append(v, fmt.Sprintf("category %q reports an incomplete total with no unknown count", name))
			}
		}
	}

	if r.Unaccounted != nil {
		if r.Unaccounted.ID != unaccountedItemID {
			v = append(v, fmt.Sprintf("residual item has id %q, expected %q", r.Unaccounted.ID, unaccountedItemID))
		}
		if p := r.Unaccounted.Load.Actual.Prov(); p != TokenResidual && p != TokenUnknown {
			v = append(v, fmt.Sprintf("residual item provenance is %s: it must be residual or unknown", p))
		}
	}

	// Potential must never reach a total. Recompute the gauge from actual
	// costs only and compare, stating the filter literally rather than
	// reusing the helper under test.
	if pot, any := r.PotentialTotal(); any && pot > 0 {
		var actualOnly int
		var complete = true
		for _, c := range r.Categories {
			for _, it := range c.Items {
				for _, tc := range it.countable() {
					if val, ok := tc.Value(); ok {
						actualOnly += val
					} else {
						complete = false
					}
				}
			}
		}
		attributed, attributedComplete := r.AttributedTotal()
		if complete != attributedComplete || (complete && actualOnly != attributed) {
			v = append(v, fmt.Sprintf("attributed total %d does not match the sum of actual costs %d: potential cost is leaking into a total", attributed, actualOnly))
		}
	}

	if r.Reconciliation.Status == ReconFailed && !r.hasCaveat("reconciliation-failed") {
		// Not itself a violation — a failed reconciliation is a reported
		// result, not a malformed report — but it must never be silent, and
		// Validate is the last gate every report passes through.
		r.AddCaveat("reconciliation-failed", r.Reconciliation.Message, SeverityBug)
	}

	r.Violations = v
	if len(v) == 0 {
		return nil
	}
	return fmt.Errorf("ctxinspect: report failed validation: %s", strings.Join(v, "; "))
}

// categoryHasUnknown reports whether any countable cost in the category is
// unknown. Used to check that an incomplete total is explained by a real
// unknown rather than by a bug in the aggregation.
func categoryHasUnknown(c Category) bool {
	for _, it := range c.Items {
		for _, tc := range it.countable() {
			if !tc.Known() {
				return true
			}
		}
	}
	return false
}

// validateItem checks one item and its children, returning the breaches found.
func validateItem(category string, it Item) []string {
	var v []string
	where := fmt.Sprintf("category %q item %q", category, it.ID)

	if strings.TrimSpace(it.ID) == "" {
		v = append(v, fmt.Sprintf("category %q has an item with no id", category))
	}
	if strings.TrimSpace(it.Label) == "" {
		v = append(v, where+" has no label")
	}

	actual := it.Load.Actual
	if actual.Prov() == TokenEstimated && strings.TrimSpace(actual.Method()) == "" {
		v = append(v, where+" carries an estimated token count with no estimator note")
	}
	if !actual.Known() && strings.TrimSpace(actual.Reason()) == "" {
		v = append(v, where+" carries an unknown token count with no reason")
	}
	if it.Load.Potential != nil {
		p := *it.Load.Potential
		if !p.Known() {
			v = append(v, where+" carries a potential cost with no value: use nil instead")
		} else if a, ok := actual.Value(); ok {
			if pv, _ := p.Value(); pv == a {
				v = append(v, where+" carries a potential cost equal to its actual cost: use nil instead")
			}
		}
	}
	if it.Load.State == Available {
		// Available means nothing put this in the prefix, so it may cost a
		// certain zero (the absence was established) or an explicit unknown
		// (it could not be). What it may never cost is a positive number: that
		// would be content the report says is both loaded and not loaded.
		//
		// The rule used to demand the zero, which forced every could-not-be-
		// established case to publish a measured zero — a summable false
		// certainty about content nobody looked at, and exactly the failure the
		// provenance model exists to prevent. See [AvailableUnknownCost].
		if val, ok := actual.Value(); ok && val != 0 {
			v = append(v, where+" is marked available but reports a positive cost: content that is not loaded cannot cost anything")
		}
	}
	if it.Content.Prov == TextAbsent && it.Content.Text != "" {
		v = append(v, where+" is marked text-absent but carries text")
	}
	if it.Content.Prov != TextAbsent && it.Content.Chars < len([]rune(it.Content.Text)) {
		v = append(v, where+" reports fewer characters than the text it carries")
	}
	if it.Content.Truncated && it.Content.Note == "" {
		v = append(v, where+" is truncated without saying so")
	}
	switch it.Lever.Kind {
	case LeverEditFile, LeverDeleteDir:
		if strings.TrimSpace(it.Lever.Path) == "" {
			v = append(v, where+" has a file/directory lever with no path")
		}
	case LeverRunCommand:
		if strings.TrimSpace(it.Lever.Command) == "" {
			v = append(v, where+" has a command lever with no command")
		}
	}
	if it.Origin == OriginHarnessBuiltin && it.Lever.Kind.Actionable() {
		v = append(v, where+" is harness-builtin but offers an actionable lever")
	}
	if it.Rollup && len(it.Children) == 0 {
		v = append(v, where+" is a rollup with no children: its cost would vanish from every total")
	}
	if it.Informational && it.Rollup {
		v = append(v, where+" is both informational and a rollup: one excludes itself from every total, the other delegates to children, and together they say nothing about where its cost is counted")
	}
	if it.Informational {
		if _, known := it.Load.Actual.Value(); known {
			v = append(v, where+" is informational but carries a known cost: a figure excluded from every total must not look like one that was counted")
		}
	}
	for _, child := range it.Children {
		v = append(v, validateItem(category, child)...)
	}
	return v
}
