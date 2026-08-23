// Package oracle is SIXGATE's G4: it compares the numbers the software puts on
// screen against a source of truth its author did not write.
//
// THE RULE THIS GATE ENFORCES. A figure may be right, it may be wrong, or it
// may be unknowable — and the only unacceptable state is the fourth one, where
// nobody can tell which. So every declared figure ends in exactly one of three
// places: it agrees with an oracle, it disagrees with an oracle and the
// disagreement is written down and explained, or there is no oracle for it and
// the user interface says so, permanently, on the screen beside the number.
// The last case is what [Parity.MustLabel] is for: G4 emits the list, G2
// asserts the labels are actually rendered, and `sixgate verdict --check`
// refuses to let the two drift apart. "We have no truth source" and "the UI
// admits it" become mechanically coupled rather than a footnote in a document
// nobody re-reads.
//
// THE STRUCTURAL RULE, same as G2's. This package may import the standard
// library and a YAML decoder — nothing else. It cannot reach the software whose
// numbers it is grading. Its two inputs are text: a frame that was recorded off
// a screen, and a file of somebody else's numbers. That is what makes an oracle
// an oracle; a comparator that could ask the product for its own answer would
// be checking the product against itself.
//
// CONSENT. Reading the canonical oracle for this repository — Claude Code's own
// /context panel — means typing into a live, billed session. Nothing in this
// package can do that. Collection is a separate, human-invoked verb, and the
// default path is offline: a person pastes the panel into a file and the
// comparator runs on the file. A gate that could quietly send keystrokes to
// somebody's terminal would be a gate nobody should run.
package oracle

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// SchemaVersion is the only `version:` an oracle declaration may carry.
const SchemaVersion = 1

// SpecFile is the declaration's filename inside the G4 gate directory.
const SpecFile = "oracle.yaml"

// Artifact filenames written into the G4 gate directory.
const (
	ParityJSONFile    = "parity.json"
	ParityMDFile      = "parity.md"
	MustLabelJSONFile = "must-label.json"
)

// Strength grades how independent an oracle really is. It is a required,
// self-incriminating field: an oracle that is weaker than it looks is worse
// than none, because it launders a guess into a confirmation.
type Strength string

// The recognised strengths, weakest last.
const (
	// StrengthIndependentAuthorship is the real thing: the numbers were
	// produced by somebody else's software, for their own purposes, and this
	// project had no hand in them.
	StrengthIndependentAuthorship Strength = "independent-authorship"
	// StrengthIndependentExtraction is weaker and must be labelled as such: the
	// numbers live in a file this project maintains, but they are read by a
	// different code path than the one under test, so the comparison still
	// catches the arithmetic drifting. It does not catch both sides being
	// wrong in the same way.
	StrengthIndependentExtraction Strength = "independent-extraction"
)

var knownStrengths = map[Strength]string{
	StrengthIndependentAuthorship: "produced by somebody else's software for their own purposes",
	StrengthIndependentExtraction: "read out of a file this project maintains, but by a different code path than the one under test",
}

// Collect names how an oracle file is obtained.
type Collect string

// The recognised collection modes.
const (
	// CollectManualPaste is the default and the only mode sanctioned on a
	// machine holding live data: a human runs the other tool and pastes its
	// output into the declared file.
	CollectManualPaste Collect = "manual-paste"
	// CollectFile means the oracle text is already on disk as recorded
	// evidence and needs no collection at all.
	CollectFile Collect = "file"
	// CollectCommand names a command a human may run, under an explicit
	// --yes, to produce the file. Nothing in this package runs it.
	CollectCommand Collect = "command"
)

// OnMissing says what happens when no oracle file exists for a figure.
type OnMissing string

// The recognised policies.
const (
	// OnMissingLabelEstimate is the coupling: an unoracled figure is allowed,
	// on condition that the screen labels it an estimate. The label becomes a
	// G2 assertion, so the condition is checked rather than promised.
	OnMissingLabelEstimate OnMissing = "LABEL_ESTIMATE"
	// OnMissingFail declares a figure that must never ship unoracled.
	OnMissingFail OnMissing = "FAIL"
)

// Reduce combines several regexp captures into one number.
type Reduce string

// The recognised reducers.
const (
	ReduceFirst Reduce = "first"
	ReduceLast  Reduce = "last"
	ReduceSum   Reduce = "sum"
	ReduceMax   Reduce = "max"
	ReduceMin   Reduce = "min"
)

var knownReduce = map[Reduce]bool{
	ReduceFirst: true, ReduceLast: true, ReduceSum: true, ReduceMax: true, ReduceMin: true,
}

// SourceKind names where one side's text comes from.
type SourceKind string

// The recognised source kinds.
const (
	// SourceScreen is a frame G1 recorded: the literal text a human read. It is
	// the preferred "ours" source, because a number that agrees with an oracle
	// in a JSON API and is blank on screen has still shipped broken.
	SourceScreen SourceKind = "screen"
	// SourceText is any other file of text, chiefly a pasted oracle panel.
	SourceText SourceKind = "text"
	// SourceJSON is a JSON document addressed with a selector.
	SourceJSON SourceKind = "json"
)

// Spec is the G4 declaration.
type Spec struct {
	Version int    `yaml:"version"`
	Slug    string `yaml:"slug"`
	// Sentence is one line explaining what this gate is comparing and why.
	Sentence string `yaml:"sentence"`
	// MinCompared is the floor on genuinely compared figures. A G4 in which
	// every figure turned out to have no oracle is not a passing gate, it is a
	// gate that did nothing; the floor is what stops "unoracled, but labelled"
	// from becoming a way to have no oracle at all.
	MinCompared int `yaml:"min_compared"`
	// Cases are the comparisons, each pairing one oracle with one recorded
	// screen or document of ours.
	Cases []Case `yaml:"cases"`
}

// Case is one oracle paired with one side of ours.
type Case struct {
	ID string `yaml:"id"`
	// Oracle describes the other party's numbers.
	Oracle OracleSource `yaml:"oracle"`
	// Ours describes where our numbers are read from.
	Ours OurSource `yaml:"ours"`
	// Figures are the quantities compared.
	Figures []Figure `yaml:"figures"`
}

// OracleSource describes the file of somebody else's numbers.
type OracleSource struct {
	// Name is the human description printed in parity.md.
	Name string `yaml:"name"`
	// Strength is how independent this oracle actually is.
	Strength Strength `yaml:"strength"`
	// Consent records whether obtaining it requires a human's explicit go-ahead
	// because collection touches a live or billed system.
	Consent string `yaml:"consent"`
	// Collect names how the file is obtained.
	Collect Collect `yaml:"collect"`
	// Command is the argv a human may run under --yes when Collect is
	// "command". Nothing in this package executes it.
	//
	// It is a LIST, never a command line. There is no shell anywhere in this
	// path: a declaration cannot smuggle a pipeline, a redirection or a second
	// command past review by hiding it inside a string, and the argv that runs
	// against a live session is literally the argv somebody read in the diff.
	Command []string `yaml:"command,omitempty"`
	// Path is the oracle file, relative to the G4 gate directory. It is allowed
	// to be absent: that is the no-oracle case, and it is handled rather than
	// hidden.
	Path string `yaml:"path"`
	// Note explains the oracle's limits in the author's own words. Required,
	// because every oracle has limits and the one nobody wrote down is the one
	// that gets over-trusted.
	Note string `yaml:"note"`
}

// OurSource describes where our own numbers are read from.
type OurSource struct {
	Kind SourceKind `yaml:"kind"`
	// Fixture and Step address a recorded G1 frame when Kind is "screen".
	Fixture string `yaml:"fixture,omitempty"`
	Step    string `yaml:"step,omitempty"`
	// AlsoFixtures are further G1 directories carrying the same frame. They do
	// not change the comparison; they widen the must-label assertions, so a
	// label proved on the in-process driver is also proved on the shipped
	// binary's pane.
	AlsoFixtures []string `yaml:"also_fixtures,omitempty"`
	// Path addresses a file directly when Kind is "text" or "json", relative to
	// the G4 gate directory.
	Path string `yaml:"path,omitempty"`
}

// Figure is one quantity, read from both sides and graded.
type Figure struct {
	ID string `yaml:"id"`
	// What is one clause naming the quantity in plain words.
	What string `yaml:"what"`
	// Ours and Theirs extract the number from each side.
	Ours   Probe `yaml:"ours"`
	Theirs Probe `yaml:"theirs"`
	// Tolerance is how far the two may differ before the row is drift.
	Tolerance Tolerance `yaml:"tolerance"`
	// OnMissing is the policy when the oracle file is absent.
	OnMissing OnMissing `yaml:"on_missing"`
	// Label is the on-screen estimate marker G2 must find when this figure ends
	// up unoracled. Required whenever OnMissing is LABEL_ESTIMATE.
	Label *Label `yaml:"label,omitempty"`
	// AcceptDrift, with a reason, downgrades a drift row from a failure to a
	// recorded, explained disagreement. It has to be argued in writing.
	AcceptDrift string `yaml:"accept_drift,omitempty"`
}

// Label is the estimate marker that must be rendered next to an unoracled
// figure, expressed as a pattern G2 will look for on a recorded frame.
type Label struct {
	// Pattern must match a line of the frame. It should name BOTH the marker
	// and the row it belongs to: a marker somewhere on the screen does not
	// label this number, and "somewhere on the screen" is how a caveat ends up
	// three panels away from the figure it excuses.
	Pattern string `yaml:"pattern"`
	// Why records what a reader loses if the marker disappears.
	Why string `yaml:"why"`
}

// Tolerance is the band two sides may differ by.
type Tolerance struct {
	// Abs is an absolute floor in the figure's own units.
	Abs float64 `yaml:"abs"`
	// Rel is a fraction of the oracle's value, 0..1.
	Rel float64 `yaml:"rel"`
	// Why explains the band. A tolerance without a reason is a number somebody
	// widened until the test passed.
	Why string `yaml:"why"`
}

// Allowed returns the permitted difference against an oracle value.
func (t Tolerance) Allowed(theirs float64) float64 {
	rel := absf(theirs) * t.Rel
	if rel > t.Abs {
		return rel
	}
	return t.Abs
}

// Probe extracts one number from one side's text.
//
// Two forms. Over text, a list of regexps each carrying one capture group,
// combined by a reducer — that covers "the panel prints it on one line" and
// "the provider records it as three fields that have to be added up" with the
// same three lines of declaration. Over JSON, a selector.
type Probe struct {
	// Patterns are Go regexps with exactly one capture group each.
	Patterns []string `yaml:"patterns,omitempty"`
	// Reduce combines the captures. Defaults to "first".
	Reduce Reduce `yaml:"reduce,omitempty"`
	// Select addresses a JSON document, e.g. "$.report.window.tokens".
	Select string `yaml:"select,omitempty"`
	// Humanize accepts figures printed for humans: "27.0k", "1.0M", "1,234",
	// and a leading "~" that marks an estimate. A screen is written for people,
	// so a comparator that cannot read "27.0k" cannot read a screen.
	Humanize bool `yaml:"humanize,omitempty"`
	// Scale multiplies the extracted value. Defaults to 1.
	Scale float64 `yaml:"scale,omitempty"`
}

// Problem is one validation failure, addressed by a schema path.
type Problem struct {
	Path string
	Msg  string
}

func (p Problem) String() string { return p.Path + ": " + p.Msg }

// Problems is an ordered list of validation failures.
type Problems []Problem

// Error renders every problem.
func (ps Problems) Error() string {
	parts := make([]string, 0, len(ps))
	for _, p := range ps {
		parts = append(parts, p.String())
	}
	return strings.Join(parts, "\n")
}

var idRe = regexp.MustCompile(`^[a-z0-9]+(?:[-_][a-z0-9]+)*$`)

// Parse decodes a declaration with unknown fields rejected.
func Parse(r io.Reader) (*Spec, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	var s Spec
	if err := dec.Decode(&s); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("empty oracle declaration")
		}
		return nil, err
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, fmt.Errorf("oracle declaration contains more than one YAML document")
	} else if !errors.Is(err, io.EOF) {
		return nil, err
	}
	return &s, nil
}

// Load reads a declaration from disk without validating it.
func Load(path string) (*Spec, error) {
	f, err := os.Open(path) //nolint:gosec // path derived from the gate tree
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	s, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// Validate checks the declaration. A nil return means it can be run.
func (s *Spec) Validate() Problems {
	var ps Problems
	add := func(path, format string, args ...any) {
		ps = append(ps, Problem{Path: path, Msg: fmt.Sprintf(format, args...)})
	}

	if s.Version != SchemaVersion {
		add("version", "must be %d, got %d", SchemaVersion, s.Version)
	}
	if strings.TrimSpace(s.Slug) == "" {
		add("slug", "required")
	}
	if strings.TrimSpace(s.Sentence) == "" {
		add("sentence", "required: say in one line what this gate compares and against what")
	}
	if s.MinCompared < 1 {
		add("min_compared", "must be at least 1: a G4 where nothing was compared is a gate that did nothing, and 'unoracled but labelled' must not become a way to have no oracle at all")
	}
	if len(s.Cases) == 0 {
		add("cases", "required: at least one oracle")
	}

	seenCase := map[string]bool{}
	seenFigure := map[string]bool{}
	for i, c := range s.Cases {
		p := fmt.Sprintf("cases[%d]", i)
		switch {
		case c.ID == "":
			add(p+".id", "required")
		case !idRe.MatchString(c.ID):
			add(p+".id", "must be kebab-case, got %q", c.ID)
		case seenCase[c.ID]:
			add(p+".id", "duplicate case %q", c.ID)
		}
		seenCase[c.ID] = true
		validateOracle(p+".oracle", c.Oracle, add)
		validateOurs(p+".ours", c.Ours, add)

		if len(c.Figures) == 0 {
			add(p+".figures", "required: a case that compares nothing is not a case")
		}
		for j, f := range c.Figures {
			fp := fmt.Sprintf("%s.figures[%d]", p, j)
			switch {
			case f.ID == "":
				add(fp+".id", "required")
			case !idRe.MatchString(f.ID):
				add(fp+".id", "must be kebab-case or snake_case, got %q", f.ID)
			case seenFigure[c.ID+"/"+f.ID]:
				add(fp+".id", "duplicate figure %q in case %q", f.ID, c.ID)
			}
			seenFigure[c.ID+"/"+f.ID] = true
			if len(strings.TrimSpace(f.What)) < 10 {
				add(fp+".what", "required: name the quantity in plain words, so parity.md can be read by somebody who has not seen the code")
			}
			validateProbe(fp+".ours", f.Ours, add)
			validateProbe(fp+".theirs", f.Theirs, add)
			if f.Tolerance.Abs < 0 || f.Tolerance.Rel < 0 {
				add(fp+".tolerance", "must not be negative")
			}
			if f.Tolerance.Rel > 1 {
				add(fp+".tolerance.rel", "is a fraction of the oracle value, 0..1, got %v", f.Tolerance.Rel)
			}
			if len(strings.TrimSpace(f.Tolerance.Why)) < 20 {
				add(fp+".tolerance.why", "required, and must be a real sentence: a tolerance without a reason is a number somebody widened until the comparison passed")
			}
			switch f.OnMissing {
			case OnMissingLabelEstimate:
				switch {
				case f.Label == nil:
					add(fp+".label", "required when on_missing is LABEL_ESTIMATE: the whole point is that an unoracled figure must be labelled an estimate ON SCREEN, and G2 cannot assert a label nobody wrote down")
				case strings.TrimSpace(f.Label.Pattern) == "":
					add(fp+".label.pattern", "required")
				default:
					if _, err := regexp.Compile(f.Label.Pattern); err != nil {
						add(fp+".label.pattern", "not a valid Go regexp: %v", err)
					}
					if len(strings.TrimSpace(f.Label.Why)) < 20 {
						add(fp+".label.why", "required: say what a reader loses if the marker disappears")
					}
				}
			case OnMissingFail:
				if f.Label != nil {
					add(fp+".label", "on_missing is FAIL, so there is no unoracled state for a label to cover; remove one or the other")
				}
			default:
				add(fp+".on_missing", "must be LABEL_ESTIMATE or FAIL, got %q", f.OnMissing)
			}
			if f.AcceptDrift != "" && len(strings.TrimSpace(f.AcceptDrift)) < 20 {
				add(fp+".accept_drift", "must be a real explanation: accepting a disagreement is a claim about the world and has to be argued in writing")
			}
		}
	}
	if len(ps) == 0 {
		return nil
	}
	return ps
}

func validateOracle(path string, o OracleSource, add func(string, string, ...any)) {
	if strings.TrimSpace(o.Name) == "" {
		add(path+".name", "required: name whose numbers these are")
	}
	if _, ok := knownStrengths[o.Strength]; !ok {
		add(path+".strength", "must be one of independent-authorship|independent-extraction, got %q — an oracle that is weaker than it looks launders a guess into a confirmation", o.Strength)
	}
	if strings.TrimSpace(o.Consent) == "" {
		add(path+".consent", "required: say whether obtaining this oracle needs a human's go-ahead (\"required\" or \"none\")")
	}
	switch o.Collect {
	case CollectManualPaste, CollectFile:
	case CollectCommand:
		if len(o.Command) == 0 {
			add(path+".command", "required when collect is \"command\": give it as an argv list, e.g. [agent-deck, session, context, ...] — there is no shell in this path, so a pipeline cannot be smuggled past review inside a string")
		}
		for i, arg := range o.Command {
			if strings.TrimSpace(arg) == "" {
				add(fmt.Sprintf("%s.command[%d]", path, i), "empty argument")
			}
		}
	default:
		add(path+".collect", "must be manual-paste|file|command, got %q", o.Collect)
	}
	if strings.TrimSpace(o.Path) == "" {
		add(path+".path", "required: name the file the oracle text lives in, even when it is not there yet — an absent declared file is the honest no-oracle state, an undeclared one is silence")
	}
	if len(strings.TrimSpace(o.Note)) < 20 {
		add(path+".note", "required, and must be a real sentence: every oracle has limits, and the one nobody wrote down is the one that gets over-trusted")
	}
}

func validateOurs(path string, o OurSource, add func(string, string, ...any)) {
	switch o.Kind {
	case SourceScreen:
		if strings.TrimSpace(o.Fixture) == "" {
			add(path+".fixture", "required for kind \"screen\": name the G1 drive directory")
		}
		if strings.TrimSpace(o.Step) == "" {
			add(path+".step", "required for kind \"screen\": name the capture step whose frame carries the figures")
		}
		if o.Path != "" {
			add(path+".path", "not used for kind \"screen\"; the frame is addressed by fixture and step")
		}
	case SourceText, SourceJSON:
		if strings.TrimSpace(o.Path) == "" {
			add(path+".path", "required for kind %q", o.Kind)
		}
	default:
		add(path+".kind", "must be screen|text|json, got %q", o.Kind)
	}
}

func validateProbe(path string, p Probe, add func(string, string, ...any)) {
	hasPatterns := len(p.Patterns) > 0
	hasSelect := strings.TrimSpace(p.Select) != ""
	switch {
	case hasPatterns && hasSelect:
		add(path, "set either patterns or select, not both")
	case !hasPatterns && !hasSelect:
		add(path, "required: one or more regexps with a single capture group, or a JSON selector")
	}
	for i, pat := range p.Patterns {
		pp := fmt.Sprintf("%s.patterns[%d]", path, i)
		re, err := regexp.Compile(pat)
		if err != nil {
			add(pp, "not a valid Go regexp: %v", err)
			continue
		}
		if n := re.NumSubexp(); n != 1 {
			add(pp, "must have exactly one capture group, has %d — the group is the number", n)
		}
	}
	if p.Reduce != "" && !knownReduce[p.Reduce] {
		add(path+".reduce", "must be first|last|sum|max|min, got %q", p.Reduce)
	}
	if p.Scale < 0 {
		add(path+".scale", "must not be negative")
	}
}

// StrengthNote returns the plain-words meaning of a strength, for a report.
func StrengthNote(s Strength) string {
	if n, ok := knownStrengths[s]; ok {
		return n
	}
	return "unrecognised"
}

func absf(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
