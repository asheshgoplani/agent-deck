package verify

import (
	"fmt"
	"math"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
)

// Group is one comparable quantity: a set of rows from the harness's own panel
// against a set of categories from an agent-deck report.
//
// Grouping rather than one-to-one mapping is the whole point. Two accountings
// of the same context can slice it differently and still agree; forcing a row
// against a category that means something else produces a permanent, meaningless
// disagreement that trains the reader to ignore the table.
type Group struct {
	// Name is the row label in the parity table.
	Name string
	// HarnessLabels are the normalized panel labels summed on the harness side.
	HarnessLabels []string
	// HarnessUsed takes the harness side from [HarnessReport.Used] instead of
	// from labelled rows, for panels that publish only an occupancy total.
	HarnessUsed bool
	// Categories are the report category names summed on the agent-deck side.
	Categories []string
	// IncludeResidual adds the report's unattributed remainder to the
	// agent-deck side.
	IncludeResidual bool
	// IncludeHistory adds the report's conversation orientation figure to the
	// agent-deck side.
	IncludeHistory bool
	// Informational marks a group that is printed but never graded. Used where
	// the two sides are related but not equivalent, so a delta is worth seeing
	// and not worth failing on.
	Informational bool
	// Note explains, in one clause, what the group compares and why it is
	// grouped that way.
	Note string
}

// Verdict is the grade of one parity row.
type Verdict uint8

const (
	// VerdictMatch: the two sides agree within tolerance.
	VerdictMatch Verdict = iota
	// VerdictDrift: the two sides disagree beyond tolerance. This is the state
	// the whole feature exists to detect.
	VerdictDrift
	// VerdictHarnessSilent: the harness printed no row for this group, so there
	// is nothing to compare. Not a failure — a version difference.
	VerdictHarnessSilent
	// VerdictOursUnknown: the report has no number for this group, so there is
	// nothing to compare. Not a failure — honest degradation.
	VerdictOursUnknown
	// VerdictInformational: printed for orientation, never graded.
	VerdictInformational
)

var verdictNames = map[Verdict]string{
	VerdictMatch:         "match",
	VerdictDrift:         "DRIFT",
	VerdictHarnessSilent: "harness silent",
	VerdictOursUnknown:   "ours unknown",
	VerdictInformational: "informational",
}

// String returns the display name.
func (v Verdict) String() string {
	if n, ok := verdictNames[v]; ok {
		return n
	}
	return "unknown"
}

// MarshalJSON encodes the verdict as its display name.
func (v Verdict) MarshalJSON() ([]byte, error) { return []byte(`"` + v.String() + `"`), nil }

// Tolerance is how far the two sides may differ before a row is graded as
// drift.
//
// Both bounds apply and the larger wins, plus the harness's own printed
// rounding for the rows involved. The absolute floor exists because a panel
// that prints "2.3k" has already thrown away 50 tokens; the percentage exists
// because agent-deck's per-item figures are character-count estimates whose
// published error bound is a percentage, not a constant.
type Tolerance struct {
	// AbsTokens is the absolute floor, in tokens.
	AbsTokens int
	// Pct is the relative bound, as a percentage of the harness figure.
	Pct float64
}

// DefaultTolerance is the shipped band: 500 tokens or 10%, whichever is larger,
// plus printed rounding.
//
// 10% is the estimator's own honesty bound. Its widest per-kind band is ±30%
// for the content kinds no corpus was available for, and ±8% for the memory
// files that dominate a real report; 10% is the figure at which a disagreement
// stops being explicable by the estimator and starts being worth reading. It is
// why [ctxinspect.Calibration] is published beside every report. Tightening this
// band without measuring more kinds would only produce drift reports about a
// known approximation.
func DefaultTolerance() Tolerance { return Tolerance{AbsTokens: 500, Pct: 10} }

// Allowed returns the tolerance in tokens for a harness-side figure carrying
// the given printed rounding slack.
func (t Tolerance) Allowed(harnessTokens, slack int) int {
	pct := int(math.Round(math.Abs(float64(harnessTokens)) * t.Pct / 100))
	allowed := t.AbsTokens
	if pct > allowed {
		allowed = pct
	}
	return allowed + slack
}

// Row is one line of the parity table.
type Row struct {
	// Group is the group's display name.
	Group string `json:"group"`
	// HarnessLabels are the panel rows that were summed, in panel order. Empty
	// when the harness side came from an occupancy total.
	HarnessLabels []string `json:"harness_labels,omitempty"`
	// Categories are the report categories that were summed.
	Categories []string `json:"categories,omitempty"`
	// Harness is the harness's figure. Valid only when HarnessKnown.
	Harness int `json:"harness"`
	// HarnessKnown reports whether the harness published this quantity.
	HarnessKnown bool `json:"harness_known"`
	// Ours is agent-deck's figure. Valid only when OursKnown.
	Ours int `json:"ours"`
	// OursKnown reports whether the report could price this quantity.
	OursKnown bool `json:"ours_known"`
	// OursComplete is false when the agent-deck side is a lower bound because a
	// contributing item had no token count.
	OursComplete bool `json:"ours_complete"`
	// Delta is ours − harness. Valid only when both sides are known.
	Delta int `json:"delta"`
	// DeltaPct is Delta as a percentage of the harness figure.
	DeltaPct float64 `json:"delta_pct"`
	// Allowed is the tolerance that was applied, in tokens.
	Allowed int `json:"allowed"`
	// Verdict grades the row.
	Verdict Verdict `json:"verdict"`
	// Note explains the grouping or the reason there is no verdict.
	Note string `json:"note,omitempty"`
}

// Status is the overall outcome of a comparison.
type Status uint8

const (
	// StatusMatch: every graded row agreed.
	StatusMatch Status = iota
	// StatusDrift: at least one graded row disagreed beyond tolerance.
	StatusDrift
	// StatusIndeterminate: nothing could be graded — the harness published
	// nothing comparable, or the report could price nothing.
	StatusIndeterminate
)

var statusNames = map[Status]string{
	StatusMatch:         "match",
	StatusDrift:         "drift",
	StatusIndeterminate: "indeterminate",
}

// String returns the display name.
func (s Status) String() string {
	if n, ok := statusNames[s]; ok {
		return n
	}
	return "indeterminate"
}

// MarshalJSON encodes the status as its display name.
func (s Status) MarshalJSON() ([]byte, error) { return []byte(`"` + s.String() + `"`), nil }

// Parity is the full comparison of a report against a harness's own panel.
type Parity struct {
	// Harness is the parsed panel.
	Harness *HarnessReport `json:"harness"`
	// Tolerance is the band that was applied.
	Tolerance Tolerance `json:"tolerance"`
	// Rows are the compared groups, in group order.
	Rows []Row `json:"rows"`
	// Status is the overall outcome.
	Status Status `json:"status"`
	// Unmapped lists panel rows that no group claimed, so a newly added row is
	// visible instead of silently excluded from every sum.
	Unmapped []string `json:"unmapped,omitempty"`
	// WindowAgrees reports whether the panel's context window matches the
	// report's. A mismatched denominator makes every percentage on both sides
	// wrong, so it is checked explicitly rather than left implicit.
	WindowAgrees bool `json:"window_agrees"`
	// WindowNote explains the window comparison.
	WindowNote string `json:"window_note,omitempty"`
}

// Drifted returns the rows graded as drift.
func (p *Parity) Drifted() []Row {
	if p == nil {
		return nil
	}
	var out []Row
	for _, r := range p.Rows {
		if r.Verdict == VerdictDrift {
			out = append(out, r)
		}
	}
	return out
}

// Compare diffs a report against a parsed harness panel.
//
// It is a pure function: given the same report and the same pane text it
// produces the same table, which is what makes the comparison testable from a
// fixture with no harness and no session.
func Compare(rep *ctxinspect.Report, h *HarnessReport, spec Spec, tol Tolerance) *Parity {
	p := &Parity{Harness: h, Tolerance: tol, Status: StatusIndeterminate}
	if rep == nil || h == nil {
		p.WindowNote = "nothing to compare"
		return p
	}

	claimed := make(map[string]bool)
	graded, drifted := 0, 0

	for _, g := range spec.Groups {
		row := compareGroup(rep, h, g, tol)
		for _, l := range row.HarnessLabels {
			claimed[l] = true
		}
		switch row.Verdict {
		case VerdictMatch:
			graded++
		case VerdictDrift:
			graded++
			drifted++
		}
		p.Rows = append(p.Rows, row)
	}

	for _, f := range h.Figures {
		if claimed[f.Label] || isContributor(f.Label, spec.ExcludedLabels) {
			continue
		}
		p.Unmapped = append(p.Unmapped, fmt.Sprintf("%s (%d tokens)", f.Raw, f.Tokens))
	}

	switch {
	case graded == 0:
		p.Status = StatusIndeterminate
	case drifted > 0:
		p.Status = StatusDrift
	default:
		p.Status = StatusMatch
	}

	p.WindowAgrees, p.WindowNote = compareWindow(rep, h)
	return p
}

// compareWindow checks that both sides are dividing by the same denominator.
func compareWindow(rep *ctxinspect.Report, h *HarnessReport) (bool, string) {
	switch {
	case h.Window <= 0 && !rep.Window.Known():
		return false, "neither side established a context-window size"
	case h.Window <= 0:
		return false, fmt.Sprintf("the panel named no window; agent-deck used %d (%s)", rep.Window.Tokens, rep.Window.Source)
	case !rep.Window.Known():
		return false, fmt.Sprintf("agent-deck established no window; the panel named %d", h.Window)
	case h.Window == rep.Window.Tokens:
		return true, fmt.Sprintf("both sides use a %d-token window (agent-deck source: %s)", h.Window, rep.Window.Source)
	default:
		return false, fmt.Sprintf("WINDOW MISMATCH: the panel says %d, agent-deck says %d (%s) — every percentage on one side is wrong",
			h.Window, rep.Window.Tokens, rep.Window.Source)
	}
}

// compareGroup builds one parity row.
func compareGroup(rep *ctxinspect.Report, h *HarnessReport, g Group, tol Tolerance) Row {
	row := Row{Group: g.Name, Categories: g.Categories, Note: g.Note}

	harnessTokens, harnessSlack, harnessKnown := harnessSide(h, g, &row)
	row.Harness, row.HarnessKnown = harnessTokens, harnessKnown

	ours, oursComplete, oursKnown := reportSide(rep, g)
	row.Ours, row.OursKnown, row.OursComplete = ours, oursKnown, oursComplete

	// The difference is computed whenever both sides exist, including for an
	// informational group: the number is worth seeing even where it is not
	// worth grading, and leaving it at its zero value would render as a
	// confident "+0" for a row nobody compared.
	if harnessKnown && oursKnown {
		row.Delta = ours - harnessTokens
		if harnessTokens != 0 {
			row.DeltaPct = float64(row.Delta) / float64(harnessTokens) * 100
		}
	}

	switch {
	case g.Informational:
		row.Verdict = VerdictInformational
	case !harnessKnown:
		row.Verdict = VerdictHarnessSilent
		if row.Note == "" {
			row.Note = "the panel printed no row for this group"
		}
	case !oursKnown:
		row.Verdict = VerdictOursUnknown
	default:
		row.Allowed = tol.Allowed(harnessTokens, harnessSlack)
		if abs(row.Delta) <= row.Allowed {
			row.Verdict = VerdictMatch
		} else {
			row.Verdict = VerdictDrift
		}
	}

	// An incomplete agent-deck side can only be a lower bound, so a "match"
	// reached by ignoring an unpriced item is not a match. Downgrade rather
	// than report an agreement that the missing number could overturn.
	if row.Verdict == VerdictMatch && !oursComplete {
		row.Verdict = VerdictOursUnknown
		row.Note = strings.TrimSpace(row.Note + " — agent-deck's side is a lower bound: an item in this group has no token count")
	}

	// The residual is anchor − attributed. When the anchor is an upper bound —
	// the measured request carried earlier turns as well as the startup
	// catalogues — so is the residual, and grading it against the harness's own
	// figure would certify an agreement between a number and an over-estimate.
	// The comparison is still worth showing; it is not worth calling a match.
	if g.IncludeResidual && rep.Anchor.IsUpperBound() {
		switch row.Verdict {
		case VerdictMatch, VerdictDrift:
			row.Verdict = VerdictOursUnknown
			row.Note = strings.TrimSpace(row.Note + " — not graded: agent-deck's anchor is an upper bound, so this group's residual is one too")
		}
	}
	return row
}

// harnessSide sums the panel rows a group claims.
func harnessSide(h *HarnessReport, g Group, row *Row) (tokens, slack int, known bool) {
	if g.HarnessUsed {
		if h.Used <= 0 {
			return 0, 0, false
		}
		return h.Used, h.UsedSlack, true
	}
	for _, label := range g.HarnessLabels {
		f, ok := h.Figure(label)
		if !ok {
			continue
		}
		tokens += f.Tokens
		slack += f.Slack
		row.HarnessLabels = append(row.HarnessLabels, f.Label)
		known = true
	}
	return tokens, slack, known
}

// reportSide sums the report categories a group claims.
//
// complete is false when any contributing figure is unknown, in which case
// tokens is a lower bound. known is false only when nothing at all could be
// summed — a group whose categories the adapter did not emit.
func reportSide(rep *ctxinspect.Report, g Group) (tokens int, complete, known bool) {
	complete = true
	for _, name := range g.Categories {
		cat, ok := rep.Category(name)
		if !ok {
			// The adapter did not emit this category. That is not an unknown
			// number: the category genuinely is not part of this report.
			continue
		}
		known = true
		// DisplayTotal, not Total: a category whose contents were never
		// established sums to a certain zero, and grading that zero against the
		// harness's own figure would certify an agreement about content nobody
		// read. It is an incomplete side, which downgrades the verdict instead.
		v, catComplete := cat.DisplayTotal()
		tokens += v
		if !catComplete {
			complete = false
		}
	}
	if g.IncludeResidual {
		if rep.Unaccounted == nil {
			complete = false
		} else if v, ok := rep.Unaccounted.Load.Actual.Value(); ok {
			tokens += v
			known = true
		} else {
			complete = false
			known = true
		}
	}
	if g.IncludeHistory {
		if rep.History == nil {
			complete = false
		} else if v, ok := rep.History.Tokens.Value(); ok {
			tokens += v
			known = true
		} else {
			complete = false
			known = true
		}
	}
	return tokens, complete, known
}

// abs returns the absolute value of an int.
func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
