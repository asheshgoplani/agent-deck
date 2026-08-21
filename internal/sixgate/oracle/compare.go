package oracle

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ParitySchema is the parity.json schema version.
const ParitySchema = 1

// MustLabelSchema is the must-label.json schema version.
const MustLabelSchema = 1

// ScreenSuffix is the extension G1 writes and this gate reads.
const ScreenSuffix = ".screen.txt"

// Verdict grades one figure.
type Verdict string

// The recognised verdicts.
const (
	// VerdictAgree: both sides produced a number and they are within tolerance.
	VerdictAgree Verdict = "agree"
	// VerdictDrift: both sides produced a number and they are not. This is a
	// failure unless the declaration accepts it in writing.
	VerdictDrift Verdict = "drift"
	// VerdictNoOracle: the oracle file does not exist, so there is nothing to
	// compare against. Allowed only under LABEL_ESTIMATE, and only because the
	// figure then joins the must-label list G2 has to satisfy.
	VerdictNoOracle Verdict = "no-oracle"
	// VerdictOursMissing: our own document does not carry the figure at all.
	// This is the blank-percentage failure wearing a different hat and it is
	// never acceptable: a number that is absent cannot be checked, cannot be
	// labelled, and is exactly what the user saw.
	VerdictOursMissing Verdict = "ours-missing"
	// VerdictTheirsMissing: the oracle file IS present and the mapping found
	// nothing in it. That is a broken declaration, not a missing oracle, and
	// silently degrading it to no-oracle would let a typo disable the gate.
	VerdictTheirsMissing Verdict = "theirs-missing"
	// VerdictError: a probe could not be evaluated.
	VerdictError Verdict = "error"
)

// Row is one figure's line in the parity table.
type Row struct {
	Case      string    `json:"case"`
	Figure    string    `json:"figure"`
	What      string    `json:"what"`
	Verdict   Verdict   `json:"verdict"`
	Ours      Reading   `json:"ours"`
	Theirs    Reading   `json:"theirs"`
	Delta     float64   `json:"delta,omitempty"`
	DeltaPct  float64   `json:"delta_pct,omitempty"`
	Allowed   float64   `json:"allowed,omitempty"`
	Tolerance Tolerance `json:"-"`
	// Accepted carries the written reason a drift was accepted.
	Accepted string `json:"accepted,omitempty"`
	// Pass is whether this row alone satisfies the gate.
	Pass bool `json:"pass"`
	// Detail explains a non-passing row in one sentence.
	Detail string `json:"detail,omitempty"`
}

// CaseResult is one oracle's outcome.
type CaseResult struct {
	ID     string       `json:"id"`
	Oracle OracleSource `json:"oracle"`
	// OraclePresent says whether the declared oracle file was on disk.
	OraclePresent bool `json:"oracle_present"`
	// OursPath is the document our numbers were read from, for a reader who
	// wants to check the evidence themselves.
	OursPath string `json:"ours_path"`
	Rows     []Row  `json:"rows"`
	Pass     bool   `json:"pass"`
}

// MustLabel is one unoracled figure the user interface has to admit to.
type MustLabel struct {
	Case   string `json:"case"`
	Figure string `json:"figure"`
	What   string `json:"what"`
	// Pattern must match a line of every frame listed below.
	Pattern string `json:"pattern"`
	Why     string `json:"why"`
	// Frames are the exact (fixture, step) frames G2 must find the marker on.
	Frames []Frame `json:"frames"`
}

// Frame addresses one recorded G1 frame.
type Frame struct {
	Fixture string `json:"fixture"`
	Step    string `json:"step"`
}

// MustLabelList is the artifact G2 consumes.
//
// It is written as its own file rather than buried inside parity.json because
// it is a contract between two gates: G4 states which numbers have no truth
// source, G2 proves the screen says so, and `verdict --check` compares the
// digest G2 recorded against the file G4 wrote. A coupling that lives in one
// document is a coupling somebody edits one half of.
type MustLabelList struct {
	Schema      int         `json:"schema"`
	Slug        string      `json:"slug"`
	GeneratedAt string      `json:"generated_at"`
	Tool        string      `json:"tool,omitempty"`
	Entries     []MustLabel `json:"entries"`
}

// Parity is G4's artifact.
type Parity struct {
	Schema      int    `json:"schema"`
	Pass        bool   `json:"pass"`
	Slug        string `json:"slug"`
	Sentence    string `json:"sentence,omitempty"`
	GeneratedAt string `json:"generated_at"`
	Tool        string `json:"tool,omitempty"`
	// MinCompared is the declared floor, reproduced so the artifact explains
	// its own pass criterion.
	MinCompared int          `json:"min_compared"`
	Compared    int          `json:"compared"`
	Cases       []CaseResult `json:"cases"`
	// MustLabel is the same list written to must-label.json.
	MustLabel []MustLabel `json:"must_label"`
	Totals    Totals      `json:"totals"`
	// Problems are the reasons the gate did not pass.
	Problems []string `json:"problems,omitempty"`
}

// Totals summarise the comparison.
type Totals struct {
	Figures      int `json:"figures"`
	Agree        int `json:"agree"`
	Drift        int `json:"drift"`
	NoOracle     int `json:"no_oracle"`
	OursMissing  int `json:"ours_missing"`
	TheirsAbsent int `json:"theirs_missing"`
	Errors       int `json:"errors"`
}

// Input carries everything Compare needs that it cannot read off disk.
type Input struct {
	Spec *Spec
	// GateDir is the G4 directory; oracle files and our own documents are
	// resolved relative to it.
	GateDir string
	// G1Root is the G1-drive directory holding the recorded frames.
	G1Root string
	Tool   string
	Now    time.Time
}

// Compare runs the declaration and produces the parity artifact.
func Compare(in Input) (*Parity, error) {
	s := in.Spec
	out := &Parity{
		Schema:      ParitySchema,
		Slug:        s.Slug,
		Sentence:    s.Sentence,
		GeneratedAt: in.Now.UTC().Format(time.RFC3339),
		Tool:        in.Tool,
		MinCompared: s.MinCompared,
		Pass:        true,
	}

	for _, c := range s.Cases {
		cr := CaseResult{ID: c.ID, Oracle: c.Oracle, Pass: true}

		oursDoc, oursPath, oursErr := readOurs(c.Ours, in.GateDir, in.G1Root)
		cr.OursPath = oursPath

		theirsPath := filepath.Join(in.GateDir, filepath.FromSlash(c.Oracle.Path))
		theirsDoc, theirsErr := os.ReadFile(theirsPath) //nolint:gosec // path derived from the gate tree
		cr.OraclePresent = theirsErr == nil

		for _, f := range c.Figures {
			row := Row{Case: c.ID, Figure: f.ID, What: f.What, Tolerance: f.Tolerance}
			switch {
			case oursErr != nil:
				row.Verdict, row.Detail = VerdictError, "our own document could not be read: "+oursErr.Error()
			default:
				row.Ours = Read(f.Ours, oursDoc, c.Ours.Kind == SourceJSON)
				if cr.OraclePresent {
					row.Theirs = Read(f.Theirs, string(theirsDoc), false)
				}
				grade(&row, f, cr.OraclePresent)
			}
			row.Pass, row.Detail = decide(row, f, row.Detail)
			if !row.Pass {
				cr.Pass = false
				out.Pass = false
			}
			if row.Verdict == VerdictNoOracle && f.OnMissing == OnMissingLabelEstimate && f.Label != nil {
				out.MustLabel = append(out.MustLabel, MustLabel{
					Case: c.ID, Figure: f.ID, What: f.What,
					Pattern: f.Label.Pattern, Why: f.Label.Why,
					Frames: framesFor(c.Ours),
				})
			}
			tally(&out.Totals, row.Verdict)
			if row.Verdict == VerdictAgree || row.Verdict == VerdictDrift {
				out.Compared++
			}
			cr.Rows = append(cr.Rows, row)
		}
		out.Cases = append(out.Cases, cr)
	}

	out.Totals.Figures = out.Totals.Agree + out.Totals.Drift + out.Totals.NoOracle +
		out.Totals.OursMissing + out.Totals.TheirsAbsent + out.Totals.Errors

	if out.Compared < s.MinCompared {
		out.Pass = false
		out.Problems = append(out.Problems, fmt.Sprintf(
			"%d figure(s) were actually compared, the declaration requires at least %d — a G4 in which every figure turned out to have no oracle is a gate that did nothing",
			out.Compared, s.MinCompared))
	}
	for _, c := range out.Cases {
		for _, r := range c.Rows {
			if !r.Pass {
				out.Problems = append(out.Problems, fmt.Sprintf("%s/%s: %s", r.Case, r.Figure, r.Detail))
			}
		}
	}
	sort.Strings(out.Problems)
	return out, nil
}

// grade fills in the verdict and the arithmetic.
func grade(row *Row, f Figure, oraclePresent bool) {
	switch {
	case row.Ours.Error != "":
		row.Verdict, row.Detail = VerdictError, "reading our own figure: "+row.Ours.Error
	case !row.Ours.Found:
		row.Verdict = VerdictOursMissing
		row.Detail = "our own document carries no such figure — a number that is absent cannot be checked, cannot be labelled, and is exactly what the user saw"
	case !oraclePresent:
		row.Verdict = VerdictNoOracle
	case row.Theirs.Error != "":
		row.Verdict, row.Detail = VerdictError, "reading the oracle: "+row.Theirs.Error
	case !row.Theirs.Found:
		row.Verdict = VerdictTheirsMissing
		row.Detail = "the oracle file is present but the mapping matched nothing in it — that is a broken declaration, not a missing oracle"
	default:
		row.Delta = row.Ours.Value - row.Theirs.Value
		if row.Theirs.Value != 0 {
			row.DeltaPct = row.Delta / absf(row.Theirs.Value) * 100
		}
		row.Allowed = f.Tolerance.Allowed(row.Theirs.Value)
		if absf(row.Delta) <= row.Allowed {
			row.Verdict = VerdictAgree
		} else {
			row.Verdict = VerdictDrift
			row.Detail = fmt.Sprintf("differs by %.0f, tolerance allows %.0f", row.Delta, row.Allowed)
		}
	}
}

// decide turns a verdict into the gate's pass/fail for that row.
func decide(row Row, f Figure, detail string) (bool, string) {
	switch row.Verdict {
	case VerdictAgree:
		return true, ""
	case VerdictDrift:
		if f.AcceptDrift != "" {
			return true, "drift accepted: " + strings.Join(strings.Fields(f.AcceptDrift), " ")
		}
		return false, detail
	case VerdictNoOracle:
		if f.OnMissing == OnMissingLabelEstimate && f.Label != nil {
			return true, "no oracle; the screen must label it an estimate, and G2 asserts that it does"
		}
		return false, "no oracle exists for this figure and the declaration says it must never ship unoracled"
	default:
		return false, detail
	}
}

func tally(t *Totals, v Verdict) {
	switch v {
	case VerdictAgree:
		t.Agree++
	case VerdictDrift:
		t.Drift++
	case VerdictNoOracle:
		t.NoOracle++
	case VerdictOursMissing:
		t.OursMissing++
	case VerdictTheirsMissing:
		t.TheirsAbsent++
	default:
		t.Errors++
	}
}

// framesFor expands a case's "ours" side into the concrete frames a label must
// appear on. The extra fixtures matter: a marker proved on the in-process
// driver's frame and absent from the shipped binary's pane would mean the
// label is a property of the harness rather than of the product.
func framesFor(o OurSource) []Frame {
	if o.Kind != SourceScreen {
		return nil
	}
	out := []Frame{{Fixture: o.Fixture, Step: o.Step}}
	for _, extra := range o.AlsoFixtures {
		out = append(out, Frame{Fixture: extra, Step: o.Step})
	}
	return out
}

// readOurs resolves the document our numbers come from.
func readOurs(o OurSource, gateDir, g1Root string) (doc, path string, err error) {
	switch o.Kind {
	case SourceScreen:
		path = filepath.Join(g1Root, o.Fixture, o.Step+ScreenSuffix)
	default:
		path = filepath.Join(gateDir, filepath.FromSlash(o.Path))
	}
	raw, err := os.ReadFile(path) //nolint:gosec // path derived from the gate tree
	if err != nil {
		return "", path, err
	}
	return string(raw), path, nil
}

// Labels builds the artifact G2 consumes.
func (p *Parity) Labels(tool string) *MustLabelList {
	return &MustLabelList{
		Schema:      MustLabelSchema,
		Slug:        p.Slug,
		GeneratedAt: p.GeneratedAt,
		Tool:        tool,
		Entries:     p.MustLabel,
	}
}
