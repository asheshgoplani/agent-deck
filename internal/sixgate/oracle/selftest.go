package oracle

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// This file is G4's own acceptance evidence, and like the Blank Detector's it
// lives in the product tree rather than in a _test.go file: SIXGATE's claim is
// that a gate runs against the artifact a user gets, without a test runner.
// `sixgate selfcheck` executes it.
//
// What it proves is the part of G4 that is easy to get quietly wrong. A
// comparator that returns zero when it cannot find a number will one day report
// that two systems agree that nothing is there; a comparator that degrades a
// broken mapping to "no oracle" turns a typo into a disabled gate; and a
// comparator that lets an unoracled figure pass without demanding a label turns
// the whole gate into a formality. Each of those is asserted below, by running
// the real Compare over a temporary tree.

// selfScreen is a frame in the shape the context inspector really renders,
// including the printed rounding a screen does to a measured figure.
const selfScreen = "" +
	" ctx · fixture · claude · window 1.0M\n" +
	" OBSERVED — anchor 27.0k measured\n" +
	"  [█░░░░░░░░░░░]  27.0k / 1.0M  (2.7%)  fixed startup overhead\n" +
	"  355     —          RECON/~est       memory files (3 items, 3 actionable)\n" +
	"    info (anchor-warm-cache): 3000 of the measured 27008 prompt tokens were served from cache.\n"

// selfOracle is a file of somebody else's numbers, in the shape a provider
// records them.
const selfOracle = `{"usage":{"input_tokens":8,"cache_creation_input_tokens":24000,"cache_read_input_tokens":3000}}`

// SelfTest proves G4's grading rules still hold. It returns nil when the
// comparator is trustworthy.
func SelfTest() error {
	dir, err := os.MkdirTemp("", "sixgate-oracle-selftest-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	g1 := filepath.Join(dir, "G1-drive", "fx")
	if err := os.MkdirAll(g1, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(g1, "04-overview"+ScreenSuffix), []byte(selfScreen), 0o600); err != nil {
		return err
	}
	gate := filepath.Join(dir, "G4-oracle")
	if err := os.MkdirAll(gate, 0o755); err != nil {
		return err
	}
	oraclePath := filepath.Join(gate, "oracle.raw.txt")
	if err := os.WriteFile(oraclePath, []byte(selfOracle), 0o600); err != nil {
		return err
	}

	run := func(s *Spec) (*Parity, error) {
		return Compare(Input{
			Spec: s, GateDir: gate,
			G1Root: filepath.Join(dir, "G1-drive"),
			Tool:   "selftest", Now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		})
	}

	var errs []string
	must := func(name string, cond bool, detail string) {
		if !cond {
			errs = append(errs, name+": "+detail)
		}
	}

	// 1. The declaration this suite is built on must validate, or every
	//    assertion below is about a shape nobody would be allowed to write.
	base := selfSpec(true)
	if ps := base.Validate(); ps != nil {
		return fmt.Errorf("the self-test's own declaration does not validate:\n%s", ps.Error())
	}

	// 2. Agreement, across printed rounding and across a three-term sum. The
	//    screen says 27.0k; the provider recorded 8 + 24000 + 3000 = 27008.
	p, err := run(base)
	if err != nil {
		return err
	}
	row := findRow(p, "window_used")
	must("agreement", row != nil && row.Verdict == VerdictAgree,
		fmt.Sprintf("a screen printing 27.0k must agree with an oracle summing to 27008 inside a ±50 rounding band, got %v", verdictOf(row)))
	must("sum reducer", row != nil && row.Theirs.Value == 27008,
		fmt.Sprintf("the oracle side must add its three terms, got %v", valueOf(row)))
	must("evidence recorded", row != nil && strings.Contains(row.Ours.Evidence, "27.0k"),
		"a parity row must quote the line its number was read from; a row without evidence is a claim")

	// 3. Drift must be detected, not absorbed. Same declaration, tolerance
	//    tightened below the printed rounding.
	tight := selfSpec(true)
	tight.Cases[0].Figures[0].Tolerance = Tolerance{Abs: 1, Why: "deliberately tighter than the screen's printed rounding, to prove drift is detected"}
	p, err = run(tight)
	if err != nil {
		return err
	}
	row = findRow(p, "window_used")
	must("drift detected", row != nil && row.Verdict == VerdictDrift && !row.Pass,
		fmt.Sprintf("an 8-token difference outside a ±1 band must be drift and must fail, got %v", verdictOf(row)))
	must("drift fails the gate", !p.Pass, "an unaccepted drift must fail the whole gate")

	// 4. An accepted drift is recorded, not hidden.
	accepted := selfSpec(true)
	accepted.Cases[0].Figures[0].Tolerance = Tolerance{Abs: 1, Why: "deliberately tighter than the printed rounding, to prove an accepted drift is still reported as drift"}
	accepted.Cases[0].Figures[0].AcceptDrift = "the screen prints one decimal place in thousands, so it cannot carry the last two digits"
	p, err = run(accepted)
	if err != nil {
		return err
	}
	row = findRow(p, "window_used")
	must("accepted drift passes", row != nil && row.Pass && row.Verdict == VerdictDrift,
		"an accepted drift must still be graded drift in the table while passing the gate; hiding it would make the acceptance invisible")

	// 5. A missing oracle FILE is the honest no-oracle state, and it must drag
	//    the figure onto the must-label list.
	noOracle := selfSpec(false)
	p, err = run(noOracle)
	if err != nil {
		return err
	}
	row = findRow(p, "memory_files")
	must("no-oracle graded", row != nil && row.Verdict == VerdictNoOracle,
		fmt.Sprintf("an absent oracle file must grade no-oracle, got %v", verdictOf(row)))
	// Both figures in this declaration lose their oracle at once, so both must
	// appear. "One of them was listed" would be the more comfortable assertion
	// and the useless one: a comparator that emits the first unoracled figure
	// and stops leaves the rest shipping unlabelled.
	must("must-label emitted for every unoracled figure", len(p.MustLabel) == 2,
		fmt.Sprintf("both figures lost their oracle, so both must be listed, got %d", len(p.MustLabel)))
	entry := findLabel(p, "memory_files")
	must("must-label names the figure", entry != nil,
		"the unoracled memory-files figure must be on the list")
	must("must-label carries frames", entry != nil && len(entry.Frames) == 1 &&
		entry.Frames[0].Fixture == "fx" && entry.Frames[0].Step == "04-overview",
		"the must-label entry must name the exact frames G2 has to check")

	// 6. LABEL_ESTIMATE without a label must be impossible to declare, and
	//    on_missing FAIL must actually fail.
	unlabelled := selfSpec(false)
	unlabelled.Cases[0].Figures[1].Label = nil
	must("label required", unlabelled.Validate() != nil,
		"a LABEL_ESTIMATE figure with no label must fail validation; G2 cannot assert a label nobody wrote down")

	hard := selfSpec(false)
	hard.Cases[0].Figures[1].OnMissing = OnMissingFail
	hard.Cases[0].Figures[1].Label = nil
	p, err = run(hard)
	if err != nil {
		return err
	}
	row = findRow(p, "memory_files")
	must("on_missing FAIL", row != nil && !row.Pass,
		"a figure declared FAIL-if-unoracled must fail when its oracle is absent")

	// 7. A broken mapping over a PRESENT oracle must not be laundered into
	//    "no oracle". That degradation would let a typo disable the gate.
	broken := selfSpec(true)
	broken.Cases[0].Figures[0].Theirs.Patterns = []string{`"no_such_field"\s*:\s*(\d+)`}
	p, err = run(broken)
	if err != nil {
		return err
	}
	row = findRow(p, "window_used")
	must("broken mapping", row != nil && row.Verdict == VerdictTheirsMissing && !row.Pass,
		fmt.Sprintf("a present oracle whose mapping matches nothing must fail as a broken declaration, got %v", verdictOf(row)))

	// 8. A figure absent from OUR OWN screen is the blank-percentage failure
	//    wearing a different hat, and must never be silently zero.
	missing := selfSpec(true)
	missing.Cases[0].Figures[0].Ours.Patterns = []string{`occupancy:\s*(\d+)`}
	p, err = run(missing)
	if err != nil {
		return err
	}
	row = findRow(p, "window_used")
	must("ours missing", row != nil && row.Verdict == VerdictOursMissing && !row.Pass && row.Ours.Value == 0 && !row.Ours.Found,
		fmt.Sprintf("a figure absent from our own frame must fail as ours-missing rather than compare as zero, got %v", verdictOf(row)))

	// 9. The floor: a run in which nothing was compared is not a passing gate.
	floor := selfSpec(false)
	p, err = run(floor)
	if err != nil {
		return err
	}
	must("min_compared floor", !p.Pass && p.Compared == 0,
		fmt.Sprintf("a run comparing %d figures must fail its min_compared floor of %d", p.Compared, floor.MinCompared))

	// 10. Humanized parsing, including the estimate marker a screen prints.
	for _, tc := range []struct {
		in   string
		want float64
	}{{"27.0k", 27000}, {"1.0M", 1e6}, {"1,234", 1234}, {"~26.5k", 26500}, {"512", 512}} {
		got, err := parseNumber(tc.in, true)
		must("humanize "+tc.in, err == nil && got == tc.want,
			fmt.Sprintf("want %v, got %v (%v)", tc.want, got, err))
	}
	if _, err := parseNumber("", true); err == nil {
		errs = append(errs, "humanize empty: an empty capture must be an error, never a zero")
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "\n"))
	}
	return nil
}

// selfSpec builds the declaration the self-test runs. withOracle controls
// whether the first case points at the oracle file that exists.
func selfSpec(withOracle bool) *Spec {
	path := "oracle.raw.txt"
	if !withOracle {
		path = "not-collected.raw.txt"
	}
	return &Spec{
		Version: SchemaVersion, Slug: "selftest", MinCompared: 1,
		Sentence: "the comparator's own evidence: agreement, drift, no-oracle and the must-label coupling",
		Cases: []Case{{
			ID: "self",
			Oracle: OracleSource{
				Name: "a recorded provider usage block", Strength: StrengthIndependentExtraction,
				Consent: "none", Collect: CollectFile, Path: path,
				Note: "a fixed document written for this self-test; it proves the comparator's arithmetic, not the product's field semantics",
			},
			Ours: OurSource{Kind: SourceScreen, Fixture: "fx", Step: "04-overview"},
			Figures: []Figure{
				{
					ID: "window_used", What: "how full the context window is said to be",
					Ours:      Probe{Patterns: []string{`\s(\S+)\s*/\s*\S+\s+\(\d`}, Humanize: true},
					Theirs:    Probe{Patterns: []string{`"input_tokens"\s*:\s*(\d+)`, `"cache_creation_input_tokens"\s*:\s*(\d+)`, `"cache_read_input_tokens"\s*:\s*(\d+)`}, Reduce: ReduceSum},
					Tolerance: Tolerance{Abs: 50, Why: "the screen prints one decimal place in thousands, so it cannot carry the last two digits"},
					OnMissing: OnMissingLabelEstimate,
					Label:     &Label{Pattern: `anchor \S+ measured`, Why: "without the marker a reader cannot tell a counted token from a guessed one"},
				},
				{
					ID: "memory_files", What: "the tokens attributed to memory files",
					Ours:      Probe{Patterns: []string{`^\s*(\d[\d,]*)\s+\S+\s+\S+\s+memory files`}, Humanize: true},
					Theirs:    Probe{Patterns: []string{`(?i)^\s*memory files\s*:?\s*([\d,]+)`}, Humanize: true},
					Tolerance: Tolerance{Abs: 0, Rel: 0.1, Why: "the estimator's own published honesty bound is ten percent"},
					OnMissing: OnMissingLabelEstimate,
					Label:     &Label{Pattern: `^\s*\d[\d,]*\s+\S+\s+\S*~est\S*\s+memory files`, Why: "without the marker on the same row the number reads as measured when it is a character-count estimate"},
				},
			},
		}},
	}
}

func findLabel(p *Parity, figure string) *MustLabel {
	for i := range p.MustLabel {
		if p.MustLabel[i].Figure == figure {
			return &p.MustLabel[i]
		}
	}
	return nil
}

func findRow(p *Parity, figure string) *Row {
	for i := range p.Cases {
		for j := range p.Cases[i].Rows {
			if p.Cases[i].Rows[j].Figure == figure {
				return &p.Cases[i].Rows[j]
			}
		}
	}
	return nil
}

func verdictOf(r *Row) string {
	if r == nil {
		return "no such row"
	}
	return string(r.Verdict)
}

func valueOf(r *Row) string {
	if r == nil {
		return "no such row"
	}
	return fmt.Sprintf("%v", r.Theirs.Value)
}
