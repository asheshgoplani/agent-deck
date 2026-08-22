// Package matrix is SIXGATE's G3: the declaration of every world the journey
// must survive, and the record of what happened in each of them.
//
// WHY A MATRIX AT ALL. G1 and G2 prove the journey holds in ONE world. The
// failure this framework exists to prevent did not live in that world — it lived
// in the first three seconds of somebody's real machine, on a screen size nobody
// drove, against data nobody had. So G3 declares the other worlds explicitly:
// every adapter, every session state, empty and enormous inputs, a narrow
// terminal, and a machine that has never run the software at all.
//
// WHY THE SPACE IS DECLARED AND THE ROWS ARE LISTED. The full cross product of
// the axes below is several hundred rows. A matrix nobody reads is not coverage,
// it is a bill. So the axes fix the vocabulary — a row naming a value no axis
// declares is a schema error — and the rows are then listed one by one, each
// with a note saying what it is for. What is deliberately NOT covered is listed
// too, as an exclusion with a written reason, so a gap appears in the report
// instead of being invisible.
//
// WHY A ROW MAY DECLARE THAT IT DIVERGES. Most matrix rows change the world in a
// way that legitimately changes the screen: a session with no memory files
// cannot show a memory-files category, and an 80-column terminal cannot paint a
// 28-cell gauge. Forcing every row to satisfy the G0 journey would leave two
// options, both bad — drop the row, or weaken the journey's assertions until the
// hardest world passes, which is exactly how a blank percentage ships. Instead a
// row states, in advance and with a reason, the step at which it expects the
// journey to stop holding. The runner then asserts that the divergence happens
// THERE and nowhere else, which is a stricter claim than "it passed": a row that
// starts failing one step earlier fails the gate.
//
// The one thing a row can never declare away is the Blank Detector. Every frame
// of every row is scanned, and a finding fails the row no matter what the row
// expected. A world is allowed to be different; it is not allowed to be blank.
package matrix

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// SchemaVersion is the only `version:` a matrix declaration may carry.
const SchemaVersion = 1

// SpecFile is the declaration's filename inside the G3 gate directory.
const SpecFile = "matrix.yaml"

// The two expectations a row may declare.
const (
	// ExpectFullJourney means every step of the G0 script must hold in this
	// world, exactly as it does in G1.
	ExpectFullJourney = "full-journey"
	// ExpectDiverges means the row expects the journey to stop holding at a
	// named step, for a written reason.
	ExpectDiverges = "diverges"
)

// The driver identifiers. A is the in-process model, B is the shipped binary in
// a real pane.
const (
	DriverA = "A"
	DriverB = "B"
)

// ConfigCold is the config-axis value naming a machine that has never run the
// software. It cannot be excluded: see [Spec.Validate].
const ConfigCold = "cold-first-run"

// Axes is the vocabulary a row may be built from.
//
// It is a struct rather than a map so the axis names are part of the schema
// instead of being whatever somebody typed. An unknown key fails to decode,
// which is the point: a misspelt axis in a coverage declaration is a coverage
// hole that looks like coverage.
type Axes struct {
	// Adapter is the harness the session runs under.
	Adapter []string `yaml:"adapter"`
	// SessionState is the lifecycle state the session is in.
	SessionState []string `yaml:"session_state"`
	// DataSize is how much context the world holds: nothing, a normal amount,
	// or a pathological amount.
	DataSize []string `yaml:"data_size"`
	// Config distinguishes a configured machine from one that has never run
	// the software.
	Config []string `yaml:"config"`
	// Terminal is the geometry, as WIDTHxHEIGHT.
	Terminal []string `yaml:"terminal"`
	// Driver names which driver executes the row.
	Driver []string `yaml:"driver"`
}

func (a Axes) named() []struct {
	name   string
	values []string
} {
	return []struct {
		name   string
		values []string
	}{
		{"adapter", a.Adapter},
		{"session_state", a.SessionState},
		{"data_size", a.DataSize},
		{"config", a.Config},
		{"terminal", a.Terminal},
		{"driver", a.Driver},
	}
}

// Row is one declared world.
type Row struct {
	// ID names the row and its artifact directory.
	ID string `yaml:"id"`
	// Note says what this row is FOR. It is required: a row nobody can explain
	// is a row nobody will maintain, and it will be the first thing deleted the
	// day the matrix gets slow.
	Note string `yaml:"note"`

	// Fixture is the recorded corpus case this world is built from. It is
	// required except on a cold-first-run row, which by definition has no data.
	Fixture string `yaml:"fixture"`

	Adapter      string `yaml:"adapter"`
	SessionState string `yaml:"session_state"`
	DataSize     string `yaml:"data_size"`
	Config       string `yaml:"config"`
	Terminal     string `yaml:"terminal"`
	Driver       string `yaml:"driver"`

	// Expect is ExpectFullJourney or ExpectDiverges.
	Expect string `yaml:"expect"`
	// DivergesAt is the step id where the journey is expected to stop holding.
	// Required, and only allowed, when Expect is ExpectDiverges.
	DivergesAt string `yaml:"diverges_at"`
	// Why explains the declared divergence. Required with DivergesAt, and long
	// enough to be a sentence: "expected to fail" is not a reason.
	Why string `yaml:"why"`

	// StopAfter ends the row's journey after that step, with StopReason
	// recorded. It scopes a row; it never relaxes an assertion, because the
	// steps it did not run have no frames and are counted as divergence.
	StopAfter  string `yaml:"stop_after"`
	StopReason string `yaml:"stop_reason"`

	// Budget overrides the wall-time cap for this row, as a Go duration.
	Budget string `yaml:"budget"`
}

// Exclusion is a declared coverage gap.
//
// Recording an exclusion is not an apology, it is the artifact. A matrix that
// silently omits gemini reads as if gemini were covered; a matrix that names it
// with a reason lets a reviewer disagree.
type Exclusion struct {
	// Axis and Value name the coordinate that is not covered.
	Axis  string `yaml:"axis"`
	Value string `yaml:"value"`
	// Why must be a real sentence.
	Why string `yaml:"why"`
}

// Budgets bound wall time.
type Budgets struct {
	// Row is the default cap for a Driver A row.
	Row string `yaml:"row"`
	// PaneRow is the default cap for a Driver B row, which builds nothing but
	// waits on a real binary booting in a real terminal.
	PaneRow string `yaml:"pane_row"`
	// UnwindGrace is how long a timed-out row is given to finish unwinding
	// after the cap expires.
	//
	// This is the safety-critical number. A row is never killed: both drivers
	// hold internally bounded waits and a deferred teardown that must prove the
	// tmux server is gone, and terminating the process mid-flight is precisely
	// how this machine leaked ~50 tmux servers and 507 of its 511
	// pseudo-terminals on 2026-07-18. So the runner stops counting the row as
	// running, then WAITS for it to unwind. If it unwinds, later rows are safe
	// to run; if it does not, the run stops and says a leak may exist.
	UnwindGrace string `yaml:"unwind_grace"`
}

// Spec is a parsed matrix declaration.
type Spec struct {
	Version int    `yaml:"version"`
	Slug    string `yaml:"slug"`
	// Sentence is one line describing what this matrix is for, reproduced into
	// the report.
	Sentence string `yaml:"sentence"`

	Budget    Budgets     `yaml:"budget"`
	Axes      Axes        `yaml:"axes"`
	Include   []Row       `yaml:"include"`
	Exclude   []Exclusion `yaml:"exclude"`
	SeamPairs []SeamPair  `yaml:"seam_pairs"`
}

// SeamPair names two rows whose frames should be compared.
//
// Two drivers that agree prove little on their own; two that disagree hand over
// a fact for free. Declaring the pairs here rather than discovering them keeps
// the comparison intentional: a pair that stops existing is a schema error, not
// a silently dropped report.
type SeamPair struct {
	// A and B are row ids. A is expected to be a Driver A row and B a Driver B
	// row, and the runner checks that.
	A string `yaml:"a"`
	B string `yaml:"b"`
	// Why says what this comparison is expected to reveal.
	Why string `yaml:"why"`
}

// Problem is one validation failure, addressed by a schema path.
type Problem struct {
	Path string
	Msg  string
}

func (p Problem) String() string { return p.Path + ": " + p.Msg }

// Problems is an ordered list of validation failures.
type Problems []Problem

// Error renders the problems as a single message.
func (ps Problems) Error() string {
	parts := make([]string, 0, len(ps))
	for _, p := range ps {
		parts = append(parts, p.String())
	}
	return strings.Join(parts, "\n")
}

var (
	idRe   = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	termRe = regexp.MustCompile(`^([0-9]{2,4})x([0-9]{2,4})$`)
)

// Parse decodes a matrix declaration with unknown fields rejected.
func Parse(r io.Reader) (*Spec, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	var s Spec
	if err := dec.Decode(&s); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("empty matrix declaration")
		}
		return nil, err
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, fmt.Errorf("matrix declaration contains more than one YAML document")
	} else if !errors.Is(err, io.EOF) {
		return nil, err
	}
	return &s, nil
}

// Load reads a matrix declaration from disk. It does not validate.
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

// KnownSteps is the set of G0 capture step ids a row may name in diverges_at or
// stop_after. It is injected rather than imported so this package stays free of
// the script schema and usable by a project whose journeys are not terminal
// journeys at all.
type KnownSteps map[string]bool

// Validate checks the declaration. A nil return means the matrix is runnable.
//
// knownSteps may be nil, in which case step references are only checked for
// being non-empty.
func (s *Spec) Validate(knownSteps KnownSteps) Problems {
	var ps Problems
	add := func(path, format string, args ...any) {
		ps = append(ps, Problem{Path: path, Msg: fmt.Sprintf(format, args...)})
	}

	if s.Version != SchemaVersion {
		add("version", "must be %d, got %d", SchemaVersion, s.Version)
	}
	if strings.TrimSpace(s.Slug) == "" {
		add("slug", "required: the feature whose journey these rows run")
	}
	if len(strings.TrimSpace(s.Sentence)) < 20 {
		add("sentence", "required: one line saying what this matrix is for")
	}

	ps = append(ps, s.validateBudgets()...)
	legal := s.validateAxes(add)
	ps = append(ps, s.validateRows(legal, knownSteps, add)...)
	ps = append(ps, s.validateExclusions(legal, add)...)
	ps = append(ps, s.validateMandatoryRows(add)...)
	ps = append(ps, s.validateSeamPairs(add)...)

	if len(ps) == 0 {
		return nil
	}
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Path < ps[j].Path })
	return ps
}

func (s *Spec) validateBudgets() Problems {
	var ps Problems
	for _, b := range []struct {
		path, value string
		required    bool
	}{
		{"budget.row", s.Budget.Row, true},
		{"budget.pane_row", s.Budget.PaneRow, true},
		{"budget.unwind_grace", s.Budget.UnwindGrace, true},
	} {
		if strings.TrimSpace(b.value) == "" {
			if b.required {
				ps = append(ps, Problem{Path: b.path, Msg: "required: a row with no wall-time cap can hang the whole gate"})
			}
			continue
		}
		d, err := time.ParseDuration(b.value)
		if err != nil {
			ps = append(ps, Problem{Path: b.path, Msg: fmt.Sprintf("not a Go duration (%q): %v", b.value, err)})
			continue
		}
		if d <= 0 {
			ps = append(ps, Problem{Path: b.path, Msg: "must be > 0"})
		}
	}
	return ps
}

// validateAxes checks the vocabulary and returns the legal value sets.
func (s *Spec) validateAxes(add func(string, string, ...any)) map[string]map[string]bool {
	legal := map[string]map[string]bool{}
	for _, ax := range s.Axes.named() {
		set := map[string]bool{}
		legal[ax.name] = set
		if len(ax.values) == 0 {
			add("axes."+ax.name, "required: an axis with no values declares no coverage")
			continue
		}
		for i, v := range ax.values {
			p := fmt.Sprintf("axes.%s[%d]", ax.name, i)
			switch {
			case strings.TrimSpace(v) == "":
				add(p, "empty value")
			case set[v]:
				add(p, "duplicate value %q", v)
			}
			set[v] = true
		}
	}
	for i, v := range s.Axes.Terminal {
		if v != "" && !termRe.MatchString(v) {
			add(fmt.Sprintf("axes.terminal[%d]", i), "must look like %q, got %q", "200x50", v)
		}
	}
	for i, v := range s.Axes.Driver {
		if v != DriverA && v != DriverB {
			add(fmt.Sprintf("axes.driver[%d]", i), "must be %q or %q, got %q", DriverA, DriverB, v)
		}
	}
	return legal
}

func (s *Spec) validateRows(legal map[string]map[string]bool, known KnownSteps, add func(string, string, ...any)) Problems {
	var ps Problems
	if len(s.Include) == 0 {
		add("include", "required: at least one row")
	}
	seen := map[string]bool{}
	for i, r := range s.Include {
		p := fmt.Sprintf("include[%d]", i)
		switch {
		case r.ID == "":
			add(p+".id", "required")
		case !idRe.MatchString(r.ID):
			add(p+".id", "must be kebab-case, got %q", r.ID)
		case seen[r.ID]:
			add(p+".id", "duplicate row id %q", r.ID)
		}
		seen[r.ID] = true

		if len(strings.TrimSpace(r.Note)) < 15 {
			add(p+".note", "required, and must say what this row is FOR: a row nobody can explain is the first one deleted when the matrix gets slow")
		}

		for _, c := range []struct{ axis, value string }{
			{"adapter", r.Adapter},
			{"session_state", r.SessionState},
			{"data_size", r.DataSize},
			{"config", r.Config},
			{"terminal", r.Terminal},
			{"driver", r.Driver},
		} {
			switch {
			case strings.TrimSpace(c.value) == "":
				add(p+"."+c.axis, "required")
			case legal[c.axis] != nil && !legal[c.axis][c.value]:
				add(p+"."+c.axis, "%q is not a declared value of axis %q; add it there first, so the vocabulary stays the schema", c.value, c.axis)
			}
		}

		if r.Config == ConfigCold {
			if strings.TrimSpace(r.Fixture) != "" {
				add(p+".fixture", "a %s row has no recorded data by definition; remove the fixture or change the config", ConfigCold)
			}
		} else if strings.TrimSpace(r.Fixture) == "" {
			add(p+".fixture", "required: name the recorded corpus case this world is built from")
		}

		switch r.Expect {
		case ExpectFullJourney:
			if r.DivergesAt != "" || r.Why != "" {
				add(p+".expect", "a %s row must not name a divergence", ExpectFullJourney)
			}
		case ExpectDiverges:
			switch {
			case strings.TrimSpace(r.DivergesAt) == "":
				add(p+".diverges_at", "required when expect is %q: the row must name the step where the journey stops holding, so a divergence one step earlier still fails", ExpectDiverges)
			case known != nil && !known[r.DivergesAt]:
				add(p+".diverges_at", "no capture step %q in the G0 journey", r.DivergesAt)
			}
			if len(strings.TrimSpace(r.Why)) < 20 {
				add(p+".why", "required, and must be a real sentence: an unexplained expected failure is how a matrix turns green by lowering the bar")
			}
		case "":
			add(p+".expect", "required: %q or %q", ExpectFullJourney, ExpectDiverges)
		default:
			add(p+".expect", "must be %q or %q, got %q", ExpectFullJourney, ExpectDiverges, r.Expect)
		}

		if r.StopAfter != "" {
			if known != nil && !known[r.StopAfter] {
				add(p+".stop_after", "no capture step %q in the G0 journey", r.StopAfter)
			}
			if len(strings.TrimSpace(r.StopReason)) < 20 {
				add(p+".stop_reason", "required with stop_after: a row that runs less of the journey must say why in the artifact")
			}
		} else if r.StopReason != "" {
			add(p+".stop_reason", "set without stop_after")
		}

		if r.Budget != "" {
			if d, err := time.ParseDuration(r.Budget); err != nil {
				add(p+".budget", "not a Go duration (%q): %v", r.Budget, err)
			} else if d <= 0 {
				add(p+".budget", "must be > 0")
			}
		}
	}
	return ps
}

func (s *Spec) validateExclusions(legal map[string]map[string]bool, add func(string, string, ...any)) Problems {
	var ps Problems
	used := s.axisValuesInUse()
	for i, e := range s.Exclude {
		p := fmt.Sprintf("exclude[%d]", i)
		if legal[e.Axis] == nil {
			add(p+".axis", "%q is not an axis", e.Axis)
			continue
		}
		if strings.TrimSpace(e.Value) == "" {
			add(p+".value", "required")
			continue
		}
		if !legal[e.Axis][e.Value] {
			add(p+".value", "%q is not a declared value of axis %q; an exclusion of something the matrix never offered records nothing", e.Value, e.Axis)
		}
		if len(strings.TrimSpace(e.Why)) < 20 {
			add(p+".why", "required, and must be a real sentence: a gap without a reason reads as an oversight, which it may well be")
		}
		if used[e.Axis][e.Value] {
			add(p+".value", "%q is excluded but is used by a row; an exclusion that contradicts the matrix is worse than none", e.Value)
		}
		// The two un-excludable coordinates. They are refused HERE, at the
		// declaration, rather than only being required below, so that the
		// error a reader gets names the rule instead of the symptom.
		if e.Axis == "config" && e.Value == ConfigCold {
			add(p+".value", "the %s row cannot be excluded: first-run modals and empty states are where \"feels broken on arrival\" lives, and no row that starts with data can reach them", ConfigCold)
		}
		if e.Axis == "driver" && e.Value == DriverB {
			add(p+".value", "driver B cannot be excluded: without one real-binary row nothing in this gate proves the shipped artifact boots at all")
		}
	}
	return ps
}

func (s *Spec) validateMandatoryRows(add func(string, string, ...any)) Problems {
	var ps Problems
	used := s.axisValuesInUse()
	if !used["config"][ConfigCold] {
		add("include", "no row has config %q. It is un-excludable: a machine that has never run the software is the only world where a first-run modal or an empty deck can appear", ConfigCold)
	}
	if !used["driver"][DriverB] {
		add("include", "no row uses driver %q. It is un-excludable: Driver A reads the model's own View() and cannot prove the shipped binary boots, wraps or releases the alt-screen", DriverB)
	}
	return ps
}

func (s *Spec) validateSeamPairs(add func(string, string, ...any)) Problems {
	var ps Problems
	byID := map[string]Row{}
	for _, r := range s.Include {
		byID[r.ID] = r
	}
	for i, sp := range s.SeamPairs {
		p := fmt.Sprintf("seam_pairs[%d]", i)
		a, aok := byID[sp.A]
		b, bok := byID[sp.B]
		if !aok {
			add(p+".a", "no row %q", sp.A)
		}
		if !bok {
			add(p+".b", "no row %q", sp.B)
		}
		if aok && a.Driver != DriverA {
			add(p+".a", "row %q runs on driver %s; a seam comparison needs the in-process side here", sp.A, a.Driver)
		}
		if bok && b.Driver != DriverB {
			add(p+".b", "row %q runs on driver %s; a seam comparison needs the real-binary side here", sp.B, b.Driver)
		}
		if aok && bok && a.Fixture != b.Fixture {
			add(p, "rows %q and %q are built from different fixtures (%q vs %q); a frame difference would say nothing about the drivers",
				sp.A, sp.B, a.Fixture, b.Fixture)
		}
		if len(strings.TrimSpace(sp.Why)) < 20 {
			add(p+".why", "required: say what this comparison is expected to reveal")
		}
	}
	return ps
}

func (s *Spec) axisValuesInUse() map[string]map[string]bool {
	used := map[string]map[string]bool{
		"adapter": {}, "session_state": {}, "data_size": {},
		"config": {}, "terminal": {}, "driver": {},
	}
	for _, r := range s.Include {
		used["adapter"][r.Adapter] = true
		used["session_state"][r.SessionState] = true
		used["data_size"][r.DataSize] = true
		used["config"][r.Config] = true
		used["terminal"][r.Terminal] = true
		used["driver"][r.Driver] = true
	}
	return used
}

// Rows returns the declared rows in declaration order.
func (s *Spec) Rows() []Row { return s.Include }

// RowByID finds a row.
func (s *Spec) RowByID(id string) (Row, bool) {
	for _, r := range s.Include {
		if r.ID == id {
			return r, true
		}
	}
	return Row{}, false
}

// Geometry parses a row's terminal axis value.
func (r Row) Geometry() (width, height int, err error) {
	m := termRe.FindStringSubmatch(r.Terminal)
	if m == nil {
		return 0, 0, fmt.Errorf("row %s: terminal %q is not WIDTHxHEIGHT", r.ID, r.Terminal)
	}
	w, _ := strconv.Atoi(m[1])
	h, _ := strconv.Atoi(m[2])
	return w, h, nil
}

// BudgetFor resolves the wall-time cap for a row: its own override, else the
// per-driver default.
func (s *Spec) BudgetFor(r Row) time.Duration {
	if r.Budget != "" {
		if d, err := time.ParseDuration(r.Budget); err == nil && d > 0 {
			return d
		}
	}
	raw := s.Budget.Row
	if r.Driver == DriverB {
		raw = s.Budget.PaneRow
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	return 3 * time.Minute
}

// UnwindGrace resolves how long a timed-out row is given to unwind.
func (s *Spec) UnwindGrace() time.Duration {
	if d, err := time.ParseDuration(s.Budget.UnwindGrace); err == nil && d > 0 {
		return d
	}
	return 3 * time.Minute
}

// CoverageSummary renders, per axis, which declared values a row actually uses
// and which are only declared. It is what turns the axes from decoration into a
// statement a reader can check.
func (s *Spec) CoverageSummary() []AxisCoverage {
	used := s.axisValuesInUse()
	excluded := map[string]map[string]string{}
	for _, e := range s.Exclude {
		if excluded[e.Axis] == nil {
			excluded[e.Axis] = map[string]string{}
		}
		excluded[e.Axis][e.Value] = e.Why
	}
	var out []AxisCoverage
	for _, ax := range s.Axes.named() {
		cov := AxisCoverage{Axis: ax.name}
		for _, v := range ax.values {
			switch {
			case used[ax.name][v]:
				cov.Covered = append(cov.Covered, v)
			case excluded[ax.name][v] != "":
				cov.Excluded = append(cov.Excluded, ValueReason{Value: v, Why: excluded[ax.name][v]})
			default:
				cov.Undeclared = append(cov.Undeclared, v)
			}
		}
		out = append(out, cov)
	}
	return out
}

// AxisCoverage is one axis's coverage: what ran, what was excluded with a
// reason, and what is neither — the third list being the only interesting one,
// because a value that is declared, unused and unexplained is a gap nobody
// decided on.
type AxisCoverage struct {
	Axis       string        `json:"axis"`
	Covered    []string      `json:"covered"`
	Excluded   []ValueReason `json:"excluded,omitempty"`
	Undeclared []string      `json:"unaccounted,omitempty"`
}

// ValueReason is an axis value with the reason it is not covered.
type ValueReason struct {
	Value string `json:"value"`
	Why   string `json:"why"`
}
